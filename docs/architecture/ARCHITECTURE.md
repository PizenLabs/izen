# Izen Architecture

Izen is a human-centered orchestration engine that separates reasoning from authority, intent from execution, and workflow state from persistent artifacts.

> LLMs reason. The Engine decides. Capabilities execute. Humans remain in control.

This document is the operational contract for that promise. Where a choice must be made between power and understanding, between speed and trust, or between cleverness and a boring, checkable rule — the boring, checkable rule wins. That is not a limitation on Izen's ambition; it is the mechanism by which Izen earns the right to be trusted with more, over time.

---

## 0. Layered Foundation

```
HUMAN            Defines intent · approves mutation · retains final control
  ↓
LLM              Reasons · analyzes · proposes · generates artifacts
  ↓
CONTROL PLANE    Validates · authorizes · restricts · tracks · invalidates
  ↓
EXECUTION PLANE  Reads · writes · executes · tests · checkpoints · rolls back
```

The **Control Plane** owns intent, workflow state, artifacts, lineage, dependency validation, capabilities, budgets, authorization, and failure classification.

The **Execution Plane** owns file reads/writes, shell commands, tests, git checkpoints, and rollback — and never independently decides whether an action is allowed.

The LLM must never be the authority that directly defines system state, execution privileges, or approval. It is a reasoning component granted controlled access by the workflow engine. **The Engine's authorization is necessary but not sufficient — for anything above a bounded micro-mutation, human approval is required in addition, not implied by it.** (§5.1)

---

## 1. Design Principles

### 1.1 Reasoning Is Not Authority

The LLM **may**: interpret intent, analyze evidence, generate hypotheses, propose plans, propose mutations, classify failures.

The LLM **may not**: grant itself capabilities, expand mutation scope, bypass checkpoints, execute arbitrary shell commands, mutate files outside authorized scope, consume stale artifacts, redefine workflow state, approve its own proposals, or silently recover from unknown failures.

```
LLM → Proposal → Engine Validation → Capability Authorization → Execution
```

### 1.2 Three Independent Control Dimensions

Workflow state, artifacts, and capabilities are tracked separately and must never collapse into one global state.

| Dimension | Answers | Values |
|---|---|---|
| **Workflow State** | Where is the system in the execution lifecycle? | `IDLE → INVESTIGATING → PLANNING → BUILDING → REVIEWING → VERIFIED` |
| **Artifacts** | What does the system know, decide, change, verify? | `Intent → Evidence → Plan → Patch → Review` |
| **Capabilities** | What is the system currently allowed to do? | `READ · WRITE · EXECUTE · TEST · PATCH · CHECKPOINT · ROLLBACK` |

### 1.3 Efficiency Is a Constraint, Not an Optimization Pass

Efficiency is not something to tune after the architecture works. It is a gate every step must pass through before it runs:

- Every retrieval step estimates token weight *before* submission to a model, not after.
- Every mutation checks budget *before* execution begins, not after diffing.
- Every artifact consumer checks a dependency hash *before* reasoning about the artifact's content, not after acting on stale data.

"Intelligent saving" means the system spends context, tokens, and mutation surface only in proportion to what the task actually requires — a greeting costs nothing, a scoped micro-fix costs a little, an architectural change costs what it costs. Cost is decided by intent complexity, never by default behavior. (§8)

### 1.4 Approval Is a Gate, Not an Inference

Reversibility (a checkpoint exists) and consent (a human said yes) are different guarantees and must not be treated as substitutes for each other. A checkpoint means a mistake can be undone *after* the fact. Approval means a human chose the action *before* it happened. Izen requires both wherever the philosophy's "AI proposes, human decides" principle applies — which is every mutation except a bounded, pre-declared micro-plan whose budget makes the blast radius small enough that the budget itself functions as pre-approval. (§5.1)

---

## 2. Core Models

### 2.1 Artifacts

Every major phase produces an artifact: `Intent → Evidence → Plan → Patch → Review`. Each artifact carries **Identity, Lifecycle, Lineage, Dependencies, Source Snapshot, Creation Metadata,** and **Storage Scope**. The artifact chain is immutable in history — new decisions create new artifacts, never silent edits to old ones.

**Identity** — globally unique, never a sequential counter: `intent_<id>`, `evidence_<id>`, `plan_<id>`, `patch_<id>`, `review_<id>` (e.g. `plan_01JZ8K7...`).

**Lineage** — deterministic provenance:

```
plan_001
  derived_from: intent_001, evidence_001

plan_002
  supersedes: plan_001
```

**Lifecycle** — the primary path now includes an explicit approval gate, plus three exceptional states:

```
DRAFT → VALIDATED → [AWAITING_APPROVAL] → AUTHORIZED → CONSUMED → ARCHIVED

STALE        dependency changed, artifact may still hold value → revalidate or regenerate
INVALIDATED  scope changed, artifact must no longer be used     → re-plan required
REJECTED     failed validation, or approval was declined
```

`AWAITING_APPROVAL` applies to any `plan` or `patch` artifact produced outside a Micro-Plan budget (§4.5). It is skipped only when the artifact's originating budget already encodes pre-approval — i.e. `/build $hot` within its declared limits. Nothing else bypasses this state. A plan sitting in `AWAITING_APPROVAL` is inert: the Engine will not transition it to `AUTHORIZED` on its own.

**Dependency Mapping** — artifacts record what they depend on (`File · Symbol · Directory · Git Commit · Environment`), each with a content hash:

```json
{
  "depends_on": [
    { "kind": "file", "id": "internal/auth/token.go", "hash": "sha256:..." },
    { "kind": "symbol", "id": "AuthMiddleware.Validate", "hash": "sha256:..." }
  ]
}
```

```
Expected Hash vs. Current Hash → MATCH → proceed
                                → MISMATCH → STALE (revalidate or replan)
```

The graph is a map, not reality — a stale plan must never be consumed blindly, and Izen must never execute an artifact against an unknown source state, including changes made outside Izen (a user edit, another process, a branch switch, external tooling).

**Storage Scope** — where an artifact lives is part of its contract, not an implementation detail:

```
~/.izen/     GLOBAL   binaries · lx engine · shared config · cross-project cache
                       (nothing project-specific, nothing that carries provenance)

./.izen/     LOCAL    session · graph index · patches · checkpoints · history · audit
                       (everything with lineage lives here; this directory is the
                        source of truth for "what did Izen know and do")
```

`ARCHIVED` artifacts persist under `./.izen/history/<artifact_id>`. Nothing artifact-shaped is ever written to `~/.izen/`. This makes local-first a property the artifact model enforces, not a convention that code has to rediscover.

### 2.2 Capability Guard

Capabilities are explicit, never inferred from what the LLM asks for, and are granular enough that a mode's actual permission boundary is representable in the Guard itself rather than layered on top of it as an unenforced convention:

```
READ · WRITE · EXECUTE · TEST · PATCH · CHECKPOINT · ROLLBACK
```

`TEST` and `PATCH` are split out from `EXECUTE`/`WRITE` specifically so a mode like `/investigate` (shell access for diagnostics and test runs, but no code mutation) is expressible as a real capability grant rather than "`EXECUTE`, scoped to test commands, by convention":

```
/investigate:  READ granted · TEST granted · EXECUTE (diagnostic-only) granted · WRITE denied · PATCH denied
/build:        READ granted · WRITE granted · TEST granted · PATCH granted · EXECUTE (restricted) granted
```

A capability can additionally be scoped:

```
EXECUTE: ALLOWED, scope: test commands only
WRITE:   ALLOWED, scope: internal/auth/*
```

```
LLM: "Run this command."
Engine: Does current policy grant this specific capability, at this scope, right now?
```

The Capability Guard is the final authority before execution — never the mode, never the prompt.

### 2.3 Mutation Budgets

Budgets serve two purposes simultaneously: they bound *risk* (blast radius of an unreviewed mutation) and they bound *cost* (tokens and time spent). Treating these as one mechanism, not two, is what keeps the architecture efficient instead of merely safe.

Dimensions: **Files, Diff Lines, Tokens, Attempts, Execution Time, Shell Commands.**

```json
{ "max_files": 3, "max_diff_lines": 150, "max_attempts": 2, "max_execution_time": "30s" }
```

Budget exhaustion is a hard boundary — `STOP → ROLLBACK or REPLAN`. The LLM may not negotiate around it. Budgets are also the mechanism by which `$hot` earns its exemption from `AWAITING_APPROVAL` (§2.1): a sufficiently small, sufficiently bounded mutation doesn't need a human in the loop *per action*, because the human already approved the shape of what "small and bounded" means when they invoked `$hot`.

### 2.4 Presentation Contract

The TUI is an industrial dashboard, not a chat window, and that distinction is an architectural guarantee, not a styling choice:

```
FIXED REGIONS (never scroll)          SCROLLABLE REGION
  workflow state                        reasoning
  active artifact id + lifecycle        human ↔ AI dialogue
  capability snapshot                   active analysis
  budget remaining
```

Anything in the fixed-region list must be readable without scrolling, at all times, regardless of how long the current reasoning trace gets. This is a rendering-layer obligation derived directly from Control Plane state (§1.2) — the presentation layer subscribes to workflow state, active artifact, and capability snapshot; it does not maintain its own copy of "what's currently true." One source of truth, two views.

---

## 3. Execution Model

### 3.1 The Five-Stage Pipeline

```
INTENT → EVIDENCE → PLAN → PATCH → REVIEW
```

Not every task requires every stage. The **Intent Router** determines the minimum valid pipeline based on complexity, risk, evidence requirement, scope size, and mutation type — and, per §1.3, the *cost* of each stage before committing to it.

### 3.2 Intent Routing → Control Flow

```
                         USER INTENT
                              │
                              ▼
                     ┌────────────────┐
                     │ INTENT ROUTER  │  complexity · risk · evidence · scope · cost
                     └───────┬────────┘
             ┌───────────────┼────────────────┐
             ▼               ▼                ▼
           LOW             MEDIUM            HIGH
             │               │                │
        /build $hot        /plan        /investigate → /plan
        (budget = pre-      │                │
         approval)          └────────────────┘
             │                       ▼
             │              AWAITING_APPROVAL
             │                       │
             │                  (human) ✔
             │                       ▼
             └───────────────→   BUILD
                                    ▼
                                 REVIEW
                                    ▼
                          FAILURE CLASSIFIER
                    ┌───────────────┼────────────────┐
                    ▼               ▼                ▼
                  REPAIR       INVESTIGATE          PLAN
```

- **Low** — bounded Micro-Plan, budget-as-approval: `Intent → Micro-Plan → Budget Validation → Checkpoint → Mutation → Review`
- **Medium** — `Intent → Plan → Awaiting Approval → Build → Review`
- **High / unknown scope** — `Intent → Evidence → Plan → Awaiting Approval → Build → Review`

---

## 4. Modes

Modes are user-facing workflow interfaces, not sources of authority — permissions are always enforced by the Capability Guard, and the columns below map 1:1 onto guardable capabilities.

| Mode | Read | Write | Test | Patch | Shell | Checkpoint | Approval |
|---|---|---|---|---|---|---|---|
| `/ask` | Yes | No | No | No | No | No | — |
| `/investigate` | Yes | No | Yes | No | diagnostic-only | Optional | — |
| `/plan` | Yes | No | No | No | No | Optional | — |
| `/build` | Yes | Yes | Yes | Yes | restricted | Required | **Required** |
| `/build $hot` | Yes | Yes | Yes | Yes | restricted | Required | Budget-as-approval |
| `/review` | Yes | No | Yes | No | testing/logging | No | — |

### 4.1 `/ask` — Information retrieval

```
/ask $prompt
/ask How does authentication flow through this repository?
```

Tool strategy: `AST/Symbol Index → fallback lx → fallback rg/grep`. Avoid scanning the whole repo unless explicitly required. Output: `Intent → Evidence → Answer`. Read-only, mutation-free, and — per §1.3 — must not trigger graph indexing or semantic retrieval for trivial input.

### 4.2 `/investigate` — Evidence discovery, root cause, diagnostics

```
/investigate $env | $trace | $diagnose | $log
```

Tool routing is deterministic: `AST Graph → lx → rg/grep → glob`. The LLM must not guess repository locations when deterministic discovery is available.

```json
{ "kind": "evidence", "facts": [], "hypotheses": [], "scope": {}, "dependencies": [] }
```

Evidence must be tagged `CONFIRMED`, `INFERRED`, or `UNKNOWN` — never an inference represented as fact.

### 4.3 `/plan` — The Decision Compiler

```
Intent + Evidence + Constraints + Scope → PLAN
```

Output: Target Scope, Impact Graph, Execution Steps, Negative Scope, Dependencies, Validation Strategy. A plan produced here always enters `AWAITING_APPROVAL` before it can be consumed by `/build`. **A plan is not code — it's an executable architectural blueprint the human signs off on.**

### 4.4 `/build` — Controlled mutation

`/build` must consume an `AUTHORIZED` Plan Artifact — which, for anything routed through `/plan`, means it already passed `AWAITING_APPROVAL`. Clarification from the user cannot silently expand scope:

```
Plan: Modify auth.go
User: Also refactor database layer.
  → Scope Change Detected → Plan Invalidated → Return to /plan → Re-approve
```

The user retains authority over intent *and* over approval; the Engine retains authority over execution boundaries.

### 4.5 `/build $hot` — Bounded Micro-Plan

`$hot` is not an unsafe bypass — it's a Micro-Plan whose budget is declared, small, and pre-approved by the act of invoking it:

```
max_files: 2 · max_diff_lines: 50 · max_context: 2000 tokens
max_attempts: 1 · checkpoint: required · scope_expansion: forbidden
```

```
Intent → Micro-Plan → Budget Validation → Checkpoint → Mutation → Review
```

If the mutation exceeds budget: `ABORT → RECOMMEND /plan` — it does not silently fall back to requesting approval mid-flight, because that would mean the human approved a different, larger action than the one they thought they were pre-approving. Every mutation still requires Intent + Scope + Micro-Plan + Checkpoint + Budget.

### 4.6 `/review` — Post-mutation verification

```
PATCH → TEST → RUNTIME VALIDATION → FAILURE CLASSIFICATION
```

Review does not automatically imply repair.

---

## 5. Authorization

The LLM never mutates the workspace directly — it generates a proposal:

```
LLM → Mutation Proposal → Engine Authorization → [Human Approval Gate] → Capability Guard → Mutation Execution
```

### 5.1 The Authorization Formula

```
MUTATION_ALLOWED =
    ValidIntent
    AND ValidScope
    AND (ValidPlan OR ValidMicroPlan)
    AND CheckpointCreated
    AND SourceHashMatch
    AND BudgetAvailable
    AND CapabilityGranted
    AND (HumanApproved OR BudgetIsPreApproval)
```

`BudgetIsPreApproval` is true only for a `ValidMicroPlan` operating within its declared, unexpanded budget (§4.5). Every other path requires `HumanApproved` explicitly — the Engine does not infer consent from the absence of an objection, from a prior approval of a *different* plan, or from silence.

The Engine evaluates, in order: workflow state validity → artifact validity → artifact authorization (including approval status) → scope containment → dependency freshness → capability policy → budget sufficiency → checkpoint existence.

```
APPROVED → APPLY MUTATION
DENIED   → REJECT → REPLAN / HUMAN
```

### 5.2 Control Plane vs. Execution Plane

```
Control Plane → Authorization → Execution Plane
```

The Execution Plane never independently decides whether an action is allowed; it only ever executes what the Control Plane has already authorized.

---

## 6. Failure Handling

### 6.1 Failure Classification

A failed review is classified before any recovery action is taken. Because classification is itself a reasoning task performed by the LLM, it inherits the same "propose, don't decide" constraint as everything else: the classifier's output is a *proposed* class, and `UNKNOWN` is the Engine's fallback whenever confidence is insufficient — not a class the LLM is trusted to rule out on its own.

| Class | Examples | Route |
|---|---|---|
| `CODE_FAILURE` | Syntax/type/compilation error, local logic error | `REPAIRING → Bounded Repair → REVIEW` |
| `ENVIRONMENT_FAILURE` | Docker unavailable, missing dependency, network/service failure | `/investigate $env` — never modify application code to fix an environment issue |
| `TEST_FAILURE` | Outdated assertion, changed spec, invalid test blueprint, missing coverage | `/plan` |
| `SCOPE_FAILURE` | Mutation touched unauthorized/unexpected scope | `IMMEDIATE ROLLBACK → REPLAN` |
| `UNKNOWN` | Cannot be confidently classified | `TERMINAL BOUNDARY → STOP → HUMAN CONTROL` |

The system must never go `UNKNOWN → LLM GUESS → MUTATE CODE`. Classification accuracy is a measured property of the system (§8.3), not an assumed one.

### 6.2 Workflow State Machine

```
IDLE → INVESTIGATING → PLANNING → BUILDING → REVIEWING → VERIFIED | FAILED
```

```
CODE_FAILURE        → REPAIRING
ENVIRONMENT_FAILURE → INVESTIGATING
TEST_FAILURE        → PLANNING
SCOPE_FAILURE        → ROLLBACK
UNKNOWN              → HUMAN_CONTROL
```

---

## 7. External Workspace Changes

The workspace can change outside Izen (user edit, external process, branch change, external tooling). Before consuming any artifact:

```
Artifact Dependency Snapshot vs. Current Workspace
  → VALID → proceed
  → STALE → revalidate → replan if necessary
```

---

## 8. Efficiency & Resource Model

This section makes §1.3 ("Efficiency Is a Constraint") checkable rather than aspirational.

### 8.1 Retrieval Cost Ordering

Structure precedes intelligence, and cheap precedes expensive:

```
AST / Symbol Graph  (near-zero cost, deterministic)
  ↓ miss
Semantic Search       (moderate cost, probabilistic)
  ↓ miss
Text Fallback (rg/grep/glob)  (cheap, resilient, imprecise)
```

Skipping a cheaper tier to reach directly for semantic search or a full-repo scan is a violation of this ordering, not a stylistic choice — it must be justified by an explicit reason recorded on the Evidence artifact (e.g. "graph index absent for this file type"), not silently taken as a shortcut.

### 8.2 Token Budget Estimation

Every retrieval step estimates the token weight of what it's about to inject *before* submitting to the model, and rejects or truncates the injection if it would exceed the current stage's budget. A trivial input (`Hi`) must never trigger graph indexing, semantic retrieval, or a repository scan — cost is proportional to the intent, not to system defaults.

### 8.3 Instrumentation as a First-Class Output

Two properties of this architecture cannot be verified by reading the design — they can only be measured in use, and both should be logged from day one rather than added later:

- **Failure classification accuracy** — how often `CODE_FAILURE`/`ENVIRONMENT_FAILURE`/`TEST_FAILURE`/`SCOPE_FAILURE` routes correctly, and how often the system lands in `UNKNOWN` when a more specific class was actually available. A classifier that over-triggers `UNKNOWN` is safe but erodes trust in the tool; one that under-triggers it is unsafe. Both are measurable.
- **Approval friction** — how often a `PLANNING → AWAITING_APPROVAL` cycle gets invalidated by scope drift (§4.4), and how often users fall back to `$hot` or a manual shell escape rather than tolerate that friction. If `$hot` usage climbs relative to `/plan` usage over time, that's a signal the approval gate or the budgets are miscalibrated — not a signal to quietly loosen the gate without looking at why.

### 8.4 Memoization and Reuse

Dependency hashes, graph queries, and symbol resolutions within a single session should be cached and invalidated only on a matching workspace-change signal (§7) — never recomputed by default "to be safe." Recomputation-by-default is itself a violation of §1.3: it spends cost the architecture has no evidence it needs.

---

## 9. Invariants

1. **No mutation without authorization** — `No Authorization = No Mutation`
2. **No mutation without scope** — `No Valid Scope = No Mutation`
3. **No mutation against stale dependencies** — `Stale Artifact = Revalidate or Replan`
4. **No silent scope expansion** — `Scope Change = Plan Invalidation`
5. **No automatic guessing after unknown failure** — `UNKNOWN = STOP`
6. **No unbounded repair loop** — `Repair = Bounded Attempts`
7. **No silent authorization** — `Authorization ≠ Approval` unless the artifact's own budget already constitutes pre-approval (§5.1); the Engine validating a plan is not the same event as a human approving it.
8. **User intent can always change**, but not silently:
   ```
   New Intent → Impact Analysis → Invalidate Affected Artifacts → Replan → Re-approve
   ```
   User authority is preserved without allowing silent workflow corruption.

---

## 10. Implementation Roadmap

Build from deterministic foundations toward intelligence — LLM integration is the *last* step, not the first.

1. **Artifact & Lineage Core** — `ArtifactID`, `ArtifactKind`, `LifecycleState` (including `AWAITING_APPROVAL`), `Lineage`, `Dependency`, `BaseArtifact`, `ArtifactRegistry`, storage-scope enforcement (`~/.izen` vs `./.izen`), lifecycle transition validation, dependency snapshots, stale detection, invalidation. *(No LLM yet.)*
2. **Capability Guard & Mutation Budget** — `READ / WRITE / EXECUTE / TEST / PATCH / CHECKPOINT / ROLLBACK`; file/diff/token/attempt/execution budgets; budget-as-pre-approval logic for micro-plans.
3. **Workflow State Machine** — `IDLE, INVESTIGATING, PLANNING, BUILDING, REVIEWING, REPAIRING, VERIFIED, ROLLBACK, HUMAN_CONTROL`, with explicit transition validation.
4. **Authorization Engine** — `Action Proposal → Policy Evaluation → Capability Validation → Artifact Validation → Human Approval Gate → Dependency Validation → Budget Validation → Authorization`.
5. **Presentation Contract** — fixed-region rendering subscribed to Control Plane state (workflow state, active artifact, capability snapshot, budget remaining), decoupled from the scrollable reasoning viewport.
6. **Execution Capabilities** — controlled read/write/test/diagnostic/checkpoint/patch/rollback operations exposed as capabilities, not unrestricted tools.
7. **LLM Reasoning Layer** — only once the control plane is stable: `Intent Interpretation → Evidence Analysis → Plan Generation → Mutation Proposal → Failure Classification`. The LLM produces proposals and artifacts; the Engine validates, gates on approval, and authorizes them.
8. **Tool Routing** — `AST, Symbol Graph, lx, rg, grep, glob, Compiler, Linter, Test Runner`, selected deterministically per the cost ordering in §8.1.
9. **Instrumentation** — failure classification accuracy and approval-friction telemetry (§8.3), wired in from the first LLM-integrated build, not added retroactively once problems are already suspected.

---

## Summary

Izen is not an autonomous agent loop. It is a controlled orchestration engine in which reasoning is separated from authority, authority is separated from execution, approval is separated from reversibility, and every meaningful action is constrained by explicit state, artifacts, capabilities, dependencies, budgets, and cost.

```
INTENT → EVIDENCE → DECISION → APPROVAL → AUTHORIZATION → MUTATION → VERIFICATION
```

**LLM ≠ Workflow Engine. Authorization ≠ Approval. Reversible ≠ Consented.** The LLM reasons. The Workflow Engine validates. The Human approves. The Capability Layer executes. Efficiency is enforced at every step, not assumed at the end. The Human remains the final source of intent and control.
