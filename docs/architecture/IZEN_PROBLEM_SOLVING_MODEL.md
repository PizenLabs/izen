# IZEN — Problem Solving Model (Pre-Phase-4 Boundary)

**Status:** Pre-Phase-4, Generality Boundary Correction complete
**Scope:** `internal/understanding`, `internal/problemsurface`, `internal/changesurface`, `internal/mutationstrategy`, `internal/problem`, `internal/adapters/web`
**Phase-4 requires:** domain-neutral evidence → bounded proposal → existing authorization → existing scheduler → existing execution

---

## 1. Izen solves problems, not file types

```text
Task
≠
File mutation
```

A file mutation is one possible engineering action. A task is a human intent that may require investigation, analysis, experimentation, mutation, verification, and observation in any combination. Izen must be able to represent a race-condition investigation, a latency profile, a flaky CI diagnostic, or a memory leak triage with the same runtime primitives it uses for a static-web redesign — without redefining the runtime for each domain.

Therefore:

- **Domain-specific reasoning** (web, Go, React, systems, performance, CI) produces **evidence and proposals**.
- **Domain-neutral runtime** (evidence, understanding, surface, bounded step, authorization, scheduler, execution, observation, state) decides what is permitted, scheduled, executed, and observed.

---

## 2. Understanding — What is currently known about the repository?

**Package:** `internal/understanding`
**Type:** `ProjectUnderstanding`
**Question:** *What is this repository/workspace, and what evidence do we currently have about it?*

`ProjectUnderstanding` is the canonical, evidence-backed semantic representation of the current workspace. It describes what the repository **IS**, not what Izen intends to modify.

**Domain-neutral contract:**
- `Root`, `Kind` (`EXISTING` / `GREENFIELD` / `UNKNOWN`), `Identity` (coarse: `go-module`, `node-app`, `mixed`, `empty`, `unknown`), `Languages`, `Components`, `Evidence`, `Confidence`, `SnapshotID`, `Digest`, `FileCount`, `CreatedAt`, `Unavailable`
- No `StaticWeb` field. No HTML/CSS/JS semantics.
- Classification derives from generic evidence only: manifests (`go.mod`, `package.json`, `Cargo.toml`…), configuration files, source extension counts, source directory topology, VCS presence, and generic structural sampling. `missing target ≠ greenfield` and `derivation failure ≠ greenfield` are preserved.
- `Components` derive from manifest identities and language evidence (representative paths per language, bounded), not from a `static-web` component.
- `Valid()` and `IsStale()` remain the lifecycle boundary: any material file change invalidates the understanding; a stale understanding must never be presented as current truth.
- `ConsiderProposal` retains the LLM boundary: model output is hypothesis, never repository truth.

**Adapter:** `internal/adapters/web.StaticWebSurface` and `web.Derive(root)` / `web.DeriveFromUnderstanding(u)` know how to interpret generic evidence as web-specific evidence (entrypoints, stylesheets, scripts, asset dirs, script/style refs). The core never imports the adapter.

---

## 3. Problem Surface — What evidence-backed areas are relevant to the problem?

**Package:** `internal/problemsurface`
**Type:** `ProblemSurface`
**Question:** *What evidence-backed areas are relevant to understanding/investigation/problem solving?*

```text
ProblemSurface
  = evidence-backed area relevant to understanding/investigation/problem solving
```

A `ProblemSurface` may reference files, directories, symbols, packages, tests, configuration, CI artifacts, logs, profiles, dependency relationships, and runtime observations — **only when those references are actually backed by evidence**. It never invents `goroutine`, `profile`, `lock`, `latency`, or `allocation` objects that the repository does not evidence.

**Minimal contract:**
- `identity/reference` (path or symbolic reference)
- `kind` / `category` (`file`, `directory`, `package`, `config`, `test`, etc.)
- `certainty` (`DIRECT` / `RELATED` / `UNKNOWN`)
- `evidence provenance` (signal keys)
- `understanding binding` (`UnderstandingDigest` + `DigestMatches` + `IsStale` semantics)

Preserves:
- `no invented targets` — every reference must be evidence-backed (explicit targets are admitted only if evidenced; otherwise dropped).
- `stale understanding → unusable surface` — digest mismatch means re-derive, never trust as current.

Has **no** authorization, capability, mutation, execution, retry, continuation, or model selection semantics. It is informational and read-only.

Derivation is generic token matching against evidenced paths/components/languages plus a bounded broad-intent supplement (`componentSurface`) for `EXISTING` projects. No `homepage` / `style` → `.css` law lives in the core; that is `internal/adapters/web` derivation.

---

## 4. Change Surface — Which relevant areas are currently candidates for mutation?

**Package:** `internal/changesurface`
**Type:** `ChangeSurface`
**Question:** *Which relevant areas are currently evidenced as candidates for mutation?*

```text
ProblemSurface
    ⊇
ChangeSurface
```

They are not the same type and must not share authority semantics. `ChangeSurface` is explicitly the **subset of the problem-relevant surface that is currently evidenced as a candidate mutation surface**.

Preserves:
- `read-only`, `informational`, `no mutation authorization`, `no execution authority`, `no mutation guard`
- `no invented targets`
- `stale understanding → unusable surface` (`DigestMatches`)

Core web keyword rules (`homepage`, `style`, `script`, `asset`, `portfolio website`, `static web` → `.css`/`.js`/`assets/`) are **not** part of domain-neutral surface semantics. If required for the static-web fixture, they are isolated as web-specific derivation in `internal/adapters/web.DeriveSurface`.

The core `Derive(intent, explicitTargets, u)` uses generic token matching against evidenced paths plus a conservative supplement for broad intents (but not for narrow intents such as `title`/`fix typo`). `knownPaths` indexes generic evidence IDs (`structure:`, `manifest:`, `config:`) and component paths, not `StaticWeb` fields.

`DeriveFromProblemSurface` is the preferred narrow transformation when a `ProblemSurface` already exists, keeping the dependency direction correct.

---

## 5. Problem-Solving Plan — What bounded reasoning/engineering steps are proposed?

**Package:** `internal/problem`
**Type:** `ProblemSolvingPlan` / `ProblemStep` / `StepKind`
**Question:** *What bounded reasoning/engineering steps are proposed?*

`ProblemSolvingPlan` represents task-level bounded reasoning structure — what should conceptually happen — rather than execution. It is:

- `pure`, `deterministic`, `proposal-level`, `read-only`
- Never authorizes, executes, retries, continues, or switches models

Minimal vocabulary (only kinds with concrete producer/consumer are implemented):

```text
INVESTIGATE
ANALYZE
EXPERIMENT
MUTATE
VERIFY
OBSERVE
```

`Derive(intent, u, ps)` inspects intent verbs: investigation-like intents (`race`, `deadlock`, `leak`, `latency`, `profile`, `flaky`, `investigate`…) yield `INVESTIGATE` / `ANALYZE` / `OBSERVE` steps that reference evidence-backed areas without forcing `CREATE`/`MODIFY`/`DELETE`/`REFACTOR`. Mutation-like intents yield `MUTATE` steps that map to the evidence-backed mutation surface. An investigation such as `investigate a race condition` is **not** forced into `MODIFY` merely because the runtime requires a mutation plan; if a mutation plan is not applicable, the problem-solving plan carries the investigation and the mutation plan remains `UNRESOLVED` with an explicit reason.

**Important boundary:**

```text
ProblemSolvingPlan = what bounded work should conceptually happen
TaskSpec / StepScheduler = how authorized execution steps are scheduled durably
```

Therefore:

```text
ProblemSolvingPlan
        ↓
proposal translation
        ↓
existing TaskSpec
        ↓
existing StepScheduler
```

No integration beyond type correctness is implemented in this phase. The plan **must not become a second scheduler**.

---

## 6. Mutation Plan — What bounded mutations are proposed?

**Package:** `internal/mutationstrategy`
**Type:** `MutationPlan` / `MutationStep` / `OperationKind` (`CREATE` / `MODIFY` / `DELETE` / `REFACTOR` / `RENAME` / `NOOP`) / `StrategyKind` (`SINGLE_STEP` / `MULTI_STEP`) / `EstimatedMutationSize`
**Question:** *What bounded mutations are proposed?*

`MutationPlan` remains **mutation-only**. It is not renamed into a general task planner.

Preserved:
- `OperationKind` / `StrategyKind` / `PlanStatus` (`READY` / `PARTIAL` / `TOO_LARGE` / `BLOCKED` / `UNRESOLVED`)
- `EstimatedMutationSize` as a structural planning estimate (`Lower`/`Expected`/`Upper`/`Confidence`), not a token prediction, provider model prediction, or domain complexity prediction. Estimation considers `candidate count`, `operation kind`, `dependency count`, `step count`, and distinct structural groups (extension/directory-based families). It does **not** require `.css count` / `.js count` / `.html count` / `asset count` as semantic concepts.
- Pure derivation: `Derive(intent, u, surface, opts)` never discovers the repository independently, never invents targets outside the evidence-backed surface, never mints authorization, never mutates the filesystem, and never invokes a model.

Removed web semantics:
- `familyOf` / `familyOrder` / `groupByFamily` / `isNarrowIntent` / `multiFamilyIntent` / `operationForIntent` / `artifactFamilyCount` no longer reference `html`/`css`/`js`/`assets`/`portfolio website`/`page title`/`static web`. Replacement uses domain-neutral structural inputs: distinct extension/directory groups, candidate relationships, explicit ordering, evidence-backed references, and bounded candidate count. When no generic dependency evidence exists, decomposition stays conservative (independent steps, alphabetical group ordering) rather than inventing a new domain abstraction.
- `operationForIntent` is verb-family only (`delete`/`rename`/`refactor`/`create` → `DELETE`/`RENAME`/`REFACTOR`/`CREATE`, otherwise `MODIFY`); no `hasStaticWeb && redesign → MODIFY` branch.
- `selectStrategy` derives from candidate count, distinct group count, and narrow-vs-broad balance without `portfolio website` / `assets` / `page title` checks. `hasMultiFamilySurface` is removed.

---

## 7. Authorization — What is actually permitted?

**Package:** `core/authorization`, `runtime/authorization`, `domain/capability`
**Question:** *What is actually permitted?*

Existing `AuthorizationEngine` remains the single mutation authorization boundary. It evaluates lifecycle, scope, capability, budget, checkpoint, and policy. No new planning abstraction mints or implies authorization. `ProblemSurface`, `ChangeSurface`, `MutationPlan`, and `ProblemSolvingPlan` carry no `Grant`, `Approve`, or `Authorize` symbols and import no authorization package.

---

## 8. Scheduler — How is authorized work scheduled and persisted?

**Package:** `runtime/scheduler`
**Question:** *How is authorized work scheduled and persisted?*

Existing `StepScheduler` remains the canonical execution scheduler. It decomposes durable `TaskSpec` targets into bounded `ExecutionStep` instances, owns `EffectiveBudget`, enforces `AcceptStep` (scheduler owns step scope; executor must not alter it), and provides `StepOutcome` (`Pending`/`Complete`/`Partial`/`Failed`) + `RecoveryContext` continuation. No second scheduler exists: no `ProblemScheduler`, `AdaptiveScheduler`, `ReasoningScheduler`, or `MutationScheduler`.

Future architecture:

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

---

## 9. Execution — What operation actually occurs?

**Package:** `runtime/engine`, `runtime/executor`, `execution`
**Question:** *What operation actually occurs?*

Execution performs only what was authorized via the capability path (`READ`/`WRITE`/`TEST`/`PATCH`/`CHECKPOINT` etc.). No new execution authority is introduced. All discovered paths remain untrusted: every path must pass normalization, containment, and symlink policy at execution time; planning-time resolution is not treated as filesystem authorization.

---

## 10. Observation — What actually happened?

**Package:** `events`, `execution`, `verification`
**Question:** *What actually happened?*

Observation describes the outcome: mutation result, output status, budget usage, dependency freshness, verification evidence, failure signals, and state fingerprint. `events.DomainEvent` remains the single truthful observation mechanism; intermediate lifecycle events never become assumed state.

---

## 11. State — What does the runtime now know to be true?

**Question:** *What does the runtime now know to be true?*

Truthful state is `Observed State + Evidence + Current durable task state`. The model transcript is never the authoritative state source. No continuation behavior is implemented in this phase; the intended future contract is documented as:

```text
Observed State
    +
Evidence
    +
Current durable task state
        ↓
future adaptive decision
```

---

## 12. Canonical Flow

```text
Human Intent
      ↓
Repository Evidence
      ↓
Project Understanding
      ↓
Problem Surface
      ↓
Problem-Solving Proposal
      ├── Investigate
      ├── Analyze
      ├── Experiment
      ├── Mutate
      └── Verify
             ↓
       Mutation Plan
             ↓
   Existing Authorization
             ↓
    Existing Scheduler
             ↓
       Execution
             ↓
       Observation
             ↓
     Truthful State
```

Branches are conceptual. No autonomous loop is built in this phase.

---

## 13. Adapter Boundary

**Adapter owns:** web-specific detection, web-specific evidence interpretation, web-specific intent heuristics, web-specific candidate derivation (`internal/adapters/web.Derive`, `DeriveFromUnderstanding`, `DeriveSurface`).

**Core owns:** evidence contracts, surface contracts, understanding identity, mutation semantics, budgets, planning invariants.

A package boundary is sufficient; no general adapter framework is introduced.

---

## 14. Non-Goals Preserved

No new execution authority, no second scheduler, no authorization redesign, no adaptive execution or continuation, no model switching or provider fallback, no `map[string]any` abstraction to hide semantic differences, no deletion of useful Phase 2/3 mutation semantics.

---

*One runtime. Many reasoning strategies. No domain-specific execution authority. Human authorization remains the root of mutation authority.*
