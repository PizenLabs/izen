# IZEN PHASE 2 — PROJECT UNDERSTANDING & CHANGE SURFACE

**Status:** implemented. `go test ./...` green, `golangci-lint` 0 issues on all touched packages.

## A. Scope

Phase 2 implemented the semantic layer between human intent and the existing
planner/proposal layers, without touching the Phase 1 authorization boundary:

```text
Human Intent → Repository Evidence → Project Understanding → Change Surface
    → Existing Planner/Proposal → Existing Authorization → Existing Execution
```

New code (all pure, deterministic, read-only derivation):

- `internal/understanding/` — the ONE canonical home for Project
  Understanding (`doc.go`, `kind.go`, `evidence.go`, `staticweb.go`,
  `understanding.go`).
- `internal/changesurface/` — the ONE canonical home for Change Surface
  (`doc.go`, `surface.go`).
- `testdata/staticweb/` — static-web acceptance fixture (`index.html`,
  `styles.css`, `script.js`, `assets/logo.svg`).
- Tests: `internal/understanding/understanding_test.go` (PU-01…07),
  `internal/changesurface/surface_test.go` (CS-01…07),
  `internal/architecture/phase2_understanding_changesurface_test.go`
  (canonical-home, no-authority, target-separation, Phase 1 regression locks).

## B. Existing Architecture

Audit of the candidate homes (§3 of the brief) and the reuse decision:

| Existing component | Reachability | Verdict |
|---|---|---|
| `project/detector` (`Detect`) | edge (UI takes `Detection` as param; no production `Detect` call site) | **reused as signal reference only** — top-level-only scoring, kept untouched |
| `discovery/recon/archetype` (`DetectArchetype`) | production (`modes/plan`, `modes/investigate`) | **reused as signal reference** — shallow top-level scan, kept untouched |
| `engine/inference` (`WorkspaceFacts`, `InferenceEngine`) | production (`modes/plan/intentcompiler`, `ui/explain`) | **reused as signal reference** — richest evidence walk, but framework detectors mix prompt keywords into workspace truth, so it cannot be the authoritative home without purification; kept untouched |
| `engine/layer1/detect` | production (`runtime/compose`, `engine/pipeline`) | **reused as signal reference** — capability-oriented, kept untouched |
| `workspace/snapshot` (`SnapshotCache`, `BuildSnapshot`) | production (`core/runtime`, plan/investigate `WithSnapshotCache`) | **lifecycle pattern reused** — digest-bound validity + refresh-on-mutation; `understanding` mirrors it with its own digest so no import cycle is created |
| `knowledge/graph` | production (`app/pipeline`, `app/compiler`) | **reused as signal reference** — recursive scan + symbols, but markers are ad-hoc (`todo_app`/`portfolio`); kept untouched |
| `runtime/harness/profile` | telemetry only | **explicitly excluded** — model-behavior telemetry is not project understanding |
| `lea` (graph store) | subordinate primitive | **excluded** — symbol store, not a classifier |
| `engine/adapter` + `staticweb.go` | pure renderers | **excluded** — render-only by contract, never detection |
| `planner/scope` | decomposition strategy scorer | **excluded** — SinglePass-vs-DAG is mutation-strategy-adjacent (out of scope); cannot host Change Surface |
| `runtime/scope*`, `execution/targets.go`, `retrieval/target_resolver` | authorization / deterministic targets / fuzzy paths | **excluded** — these ARE the Target Resolution and Authorization concepts Phase 2 must stay distinct from |

No existing type could host "plausibly relevant area" semantics without
collapsing Target Resolution, Authorization Scope, or Mutation Strategy
into one object — which §10 forbids. Two minimal, dependency-free packages
were therefore created instead of a `ChangeSurfaceManager/Engine` or
`ProjectUnderstandingEngine` orchestration layer. Nothing existing was
moved, renamed, or re-responsibilitied.

## C. Project Understanding Model

Canonical type: `understanding.ProjectUnderstanding` in
`internal/understanding` (stdlib-only package).

```text
Root, Kind (EXISTING/GREENFIELD/UNKNOWN), Identity (coarse label),
Languages, Components, Evidence (auditable kind:id records),
StaticWeb (*StaticWebSurface), Confidence [0,1],
SnapshotID + Digest (lifecycle binding), FileCount, CreatedAt,
Unavailable (+Reason) on discovery failure
```

Lifecycle: `Derive(root)` binds the understanding to `Digest`
(path+size surface hash) and `SnapshotID`. `Valid()` gates consumption;
`IsStale()` recomputes the digest — any added/removed/resized file renders
prior understanding stale. Stale/unavailable understanding is never current
truth; re-derive instead. This mirrors `workspace/snapshot` refresh
semantics without importing it (no cycle, no OCC redesign).

## D. Classification Semantics

- **EXISTING** — any recognized manifest (`go.mod`, `package.json`,
  `Cargo.toml`, …) OR corroborated structure (HTML entrypoint, ≥2 source
  files, config + sources). Implemented in `classify()`.
- **GREENFIELD** — ONLY a genuinely empty workspace or trivial-only files
  (`README`/`LICENSE`/`.gitignore`). A missing single target file NEVER
  implies GREENFIELD (PU-04: `package.json`+`src/` without `index.html` is
  EXISTING).
- **UNKNOWN** — inaccessible root, unrecognized content, or insufficient
  evidence. Derivation failure is UNKNOWN, never GREENFIELD (`unavailable()`).

## E. Evidence Model

Repository evidence only; no LLM call exists in the derivation path:

- manifests (weighted by indicator strength), framework/build configs,
  declared `package.json` dependencies (bounded read),
  language-by-extension counts, source-directory topology, HTML
  script/stylesheet references (regex over already-scanned HTML, capped),
  asset directories, and already-available VCS presence.
- Every record is `Evidence{Kind, ID, Detail, Weight}` with a canonical
  `Key()` (`manifest:go.mod`, `structure:index.html`, …).
- Confidence is the clamped weight sum (two-decimal deterministic); weak
  evidence stays visibly uncertain — coarse `static-web` over fabricated
  framework stacks (no false precision, §14).

## F. Static Web Support

`scanStaticWeb` builds `StaticWebSurface` without any Go-oriented symbol
extractor fiction: HTML/CSS/JS-TS file lists, entrypoints (`index.html`
first), asset dirs (`assets/images/public/static/…`), `<script src>` /
`<link href>` references, and `HasPackageJSON` as a toolchain hint (never
a framework claim). Proven by `testdata/staticweb` (PU-05) and the
missing-`index.html` variant (PU-04).

## G. Change Surface

Canonical type: `changesurface.ChangeSurface` in `internal/changesurface`
(imports stdlib + `internal/understanding` only).

```text
Derive(intent, explicitTargets, understanding) → ChangeSurface{
  Status (RESOLVED/PARTIAL/UNRESOLVED),
  Candidates[]{Path, Certainty (DIRECT/RELATED/UNKNOWN), Reason, Evidence},
  Evidence (provenance keys), UnderstandingDigest, IntentSummary,
  UnresolvedReason }
```

Semantics: evidenced explicit `@targets` → DIRECT; intent keywords
(homepage/style/script/assets families) mapped ONLY onto evidenced paths;
broad read intents (`review/explore/…`) over EXISTING → capped RELATED
component surface; UNKNOWN/invalid understanding → empty UNRESOLVED (never
fabricated targets). Surfaces bind to the understanding digest
(`DigestMatches`); a digest mismatch means re-derive.

## H. Boundary Separation

```text
Target Resolution  ≠ Change Surface ≠ Authorization Scope ≠ Mutation Guard
```

- Target Resolution answers "what concrete target did the user refer to?"
  (`execution.ResolveTargetSet` — still fails closed on unevidenced
  non-template targets; untouched).
- Change Surface answers "what repository area is structurally relevant?"
  (candidates + evidence + certainty; no operations, no grants).
- Authorization Scope answers "what may Izen mutate?" (Phase 1 grants;
  the new packages import nothing that mints them).
- Mutation Guard answers "can this mutation cross the safety boundary?"
  (OCC/ScopeGuard; untouched).
- Pinned by `TestPhase2_TargetResolutionStaysDistinct`,
  `TestPhase2_ChangeSurfaceNeverAuthorizes`, and the
  `assertNoForbiddenImports/Idents` sweeps (stdlib-only understanding;
  surface may import only the understanding home).

## I. LLM Boundary

No model call exists in either package. Model input enters ONLY as
`understanding.ModelProposal{Claim, Basis}` (hypothesis), and
`ConsiderProposal` ALWAYS returns the understanding unchanged with
`ProposalDisposition{Accepted: false}` — evidence-backed state is retained,
the proposal stays hypothesis (PU-07). Repository evidence is the sole
source of structural truth.

## J. Tests

| ID | Test | Result |
|---|---|---|
| PU-01 | existing evidence → EXISTING | pass |
| PU-02 | empty/trivial workspace → GREENFIELD | pass |
| PU-03 | inaccessible/unrecognized → UNKNOWN (+Unavailable) | pass |
| PU-04 | missing `index.html` ≠ GREENFIELD (node + static variants) | pass |
| PU-05 | fixture → static-web surface (entrypoint/CSS/JS/assets/refs) | pass |
| PU-06 | evidence-backed (traceable records, confidence, digest, coarse identity) | pass |
| PU-07 | model proposal cannot override evidence | pass |
| CS-01 | intent + understanding → RESOLVED surface (`index.html` DIRECT) | pass |
| CS-02 | surface carries evidence/provenance + digest binding | pass |
| CS-03 | surface grants nothing (explicit paths, no authority fields) | pass |
| CS-04 | explicit target admitted only with evidence; unevidenced dropped | pass |
| CS-05 | no mutation-operation verbs in candidates | pass |
| CS-06 | UNKNOWN/invalid understanding → empty UNRESOLVED | pass |
| CS-07 | material change → stale understanding; surface digest mismatch | pass |
| ARCH | canonical homes exist; no rival orchestration homes | pass |
| ARCH | understanding imports/idents never touch authority | pass |
| ARCH | change surface imports/idents never touch authority or strategy | pass |
| AUTH-REG-01/02 | derivation leaves workspace untouched; no approvable artifact | pass |
| AUTH-REG-03/04 | Phase 1 gateway contract intact; `go test ./...` fully green | pass |

Lint: `golangci-lint run` on `internal/understanding/...`,
`internal/changesurface/...`, `internal/architecture/...` → 0 issues.

## K. Non-Goals

Phase 2 did NOT implement (confirmed — no such symbols exist in the new
packages, pinned by the ident sweeps): Mutation Strategy, patch strategy,
`EstimatedMutationSize`, token/step budgeting, "bound the step, not the
task", adaptive execution, continuation pipeline, auto-recovery redesign,
autonomous retry policy, new execution authority, new authorization
framework, new capability vocabulary, new runtime/executor, permission
escalation, `$prompt`/`$hot` semantic changes, OCC redesign, ScopeGuard
redesign, PatchManager redesign, ExecutionEvidence redesign, shell
authorization changes, or test/build authorization changes.

## L. Remaining Risks

1. **Digest is size-based, not content-based.** Same-size content edits do
   not invalidate understanding (unlike `workspace/snapshot` content
   hashes). A later phase may adopt content hashing; the lifecycle seam
   (`Digest`/`IsStale`) already supports swapping the function.
2. **No planner consumption wired.** Understanding → Planner is currently
   available but uncalled; wiring it later must preserve the
   informational-only direction (no authority edge).
3. **Keyword derivation is heuristic.** Intent→surface keyword families are
   conservative by design (evidence-gated), but non-English or
   domain-specific intents degrade to UNRESOLVED/PARTIAL rather than
   resolving — intended, but planners must handle UNRESOLVED gracefully.
4. **Headless divergence (Phase 1 §J) persists.** Untouched by Phase 2.
5. **Static-web refs are regex-based.** Malformed markup yields fewer refs
   (UNKNOWN-leaning), never errors — acceptable for structural purposes.
