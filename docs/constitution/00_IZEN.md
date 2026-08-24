# Izen Constitution

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.1.0 |
| Role | Constitutional Root |

## 1. Identity

Izen is a human-centered orchestration system for reasoning, planning, execution, and verification across a software workspace.

Izen exists to place complex model-driven work inside an explicit system of boundaries, contracts, evidence, and human control.

Izen is **not** defined by a particular model, agent implementation, user interface, provider, execution strategy, or programming language — those are implementation mechanisms.

The constitutional identity of Izen is defined by the relationships between:

- Human intent
- System context
- Model reasoning
- Proposed work
- Authorization
- Execution
- Evidence
- Verification

## 2. Purpose

Izen exists to make model-driven work:

- understandable
- controllable
- observable
- reversible where possible
- evidence-based
- faithful to human intent

The system must prefer explicit authority and observable state over implicit behavior.

- A successful model response is **not** equivalent to a successful system operation.
- A proposed action is **not** equivalent to an authorized action.
- An authorized action is **not** equivalent to an executed action.
- An executed action is **not** equivalent to a verified result.

These distinctions are fundamental to Izen.

## 3. Constitutional Stance

Izen treats model output as **untrusted input** to a larger runtime system.

Models may reason, interpret, propose, transform, and produce artifacts. Models do **not** inherently own:

- workspace authority
- execution authority
- filesystem authority
- capability grants
- system state

Likewise:

- Intent does not grant execution authority.
- Workspace context does not grant permissions.
- Agent identity does not grant capabilities.
- Proposal does not constitute authorization.
- UI state does not constitute runtime state.
- Model output does not constitute an artifact until validated.
- Artifact existence does not constitute mutation.
- Mutation does not constitute verification.

> Authority must be explicit.

## 4. Human Primacy

The human remains the source of intent and the ultimate authority over consequential system behavior.

Izen may interpret, decompose, reason about, and execute work according to explicit system contracts. However, the system must not silently convert contextual information into authority.

- Human intent may be resolved into system intent.
- System intent may produce proposals.
- Proposals may become authorized operations only through the applicable autonomy and authorization rules.
- Execution must occur only through the authoritative runtime boundary.

## 5. Fundamental Distinctions

Izen maintains the following distinctions as constitutional boundaries:

| Concept A | | Concept B |
|---|---|---|
| Human Intent | ≠ | System Intent |
| Intent | ≠ | Authorization |
| Workspace | ≠ | Permission |
| Agent | ≠ | Authority |
| Proposal | ≠ | Capability Grant |
| Model Output | ≠ | Artifact |
| Artifact | ≠ | Mutation |
| Execution | ≠ | Verification |
| Runtime State | ≠ | UI State |
| Failure | ≠ | Recovery |
| Retry | ≠ | Contract Change |

Implementations must not collapse these concepts merely because doing so is operationally convenient.

## 6. Authority Model

Authority in Izen is layered. At minimum, the system distinguishes:

```
Human
  ↓
Intent
  ↓
Decision
  ↓
Proposal
  ↓
Capability Grant
  ↓
Runtime Execution
  ↓
Evidence
  ↓
Verification
```

Each stage has a distinct responsibility. No lower-level component may implicitly acquire authority from a higher-level contextual object.

In particular: an Agent may propose work, but the Agent is not the owner of execution capability. Capability is granted to the trusted execution boundary and enforced by the Runtime Host.

## 7. Runtime Truth

Izen treats runtime evidence as the source of truth for consequential system state. The system must distinguish between:

- what was requested
- what was proposed
- what was authorized
- what was attempted
- what actually changed
- what provider usage was actually reported
- what was subsequently verified

The UI is a projection of runtime truth. The UI must not manufacture domain state, predict successful mutation, or replace authoritative evidence with local estimates.

## 8. Execution Authority

All consequential mutations must cross an explicit execution boundary. There must be **one authoritative production execution path**.

- Execution strategy is a runtime concern.
- Artifact representation is a data concern.

These concepts must not become interchangeable merely because a particular artifact currently maps conveniently to a particular strategy.

No implicit execution strategy fallback is permitted. When an execution contract cannot satisfy a failure condition, the system must either:

1. construct a materially different recovery contract, or
2. escalate the condition.

Changing only labels, identifiers, prompts, or retry metadata does **not** constitute recovery.

## 9. Recovery Integrity

Recovery is a change in operational contract, not merely another attempt.

For a failed contract `N`:

```
Contract(N+1) ≠ Contract(N)
```

when the original contract cannot satisfy the observed failure condition.

A valid recovery may change, where appropriate:

- execution strategy
- artifact representation
- target scope
- token or resource constraints
- validation requirements
- escalation behavior

The exact recovery mechanism belongs to the execution and autonomy specifications. The constitutional requirement is that recovery must be **materially meaningful**.

## 10. Evidence Before Assertion

Izen must not represent an operation as successful solely because a model produced a response or an execution function returned without an immediately visible error.

Consequential state must be grounded in evidence:

- For filesystem mutation, evidence should describe the actual post-execution state rather than merely the intended patch.
- For provider usage, authoritative provider-reported usage takes precedence over local estimation whenever available.
- For verification, the result of the verification process is distinct from the mutation result itself.

## 11. Failure Transparency

Failures are system state, not presentation defects.

Izen must preserve enough information to distinguish at least:

- model failure
- parsing failure
- artifact validation failure
- authorization failure
- execution failure
- recovery failure
- verification failure
- unavailable or uncertain evidence

The system must not hide architectural failure behind successful-looking UI state.

## 12. Explicitness Over Implicitness

When two behaviors are possible, Izen prefers the behavior whose authority, contract, state transition, and evidence are explicit.

Implicit behavior is especially dangerous when it affects:

- authorization
- execution
- mutation
- recovery
- provider accounting
- reported system state

Convenience must not silently become authority.

## 13. Reversibility and Safety

Where practical, consequential operations should remain observable, attributable, and reversible.

Izen should preserve enough information to answer:

- What was requested?
- Why was it executed?
- Under which contract?
- With which capability?
- What actually changed?
- What evidence proves the change?
- What verification followed?

The system should favor bounded and inspectable operations over opaque mutations.

## 14. Constitutional Hierarchy

This document is the root of the Izen Constitution. The remaining constitutional documents refine, but must not contradict, the principles established here:

```
00_IZEN.md
    │
    ├── 01_PHILOSOPHY.md
    ├── 02_SYSTEM_MODEL.md
    ├── 03_WORKSPACES.md
    ├── 04_INTENT.md
    ├── 05_AUTONOMY.md
    └── 06_EXECUTION.md
```

Lower-level documents may define more specific contracts. They must not silently redefine constitutional authority. Implementation details belong outside the Constitution.

## 15. Constitutional Invariants

The following invariants are foundational:

| ID | Name | Statement |
|---|---|---|
| I-001 | Human Intent | Human intent is the origin of consequential work. |
| I-002 | No Implicit Authority | Context, intent, workspace, model output, or agent identity cannot implicitly grant execution authority. |
| I-003 | Agent Boundary | The Agent may reason and propose but does not inherently own execution capability. |
| I-004 | Artifact Boundary | Raw model output is not an executable artifact until it passes the applicable parsing and validation boundary. |
| I-005 | Execution Authority | Consequential mutation occurs only through the authoritative runtime execution boundary. |
| I-006 | Recovery Integrity | A recovery operation must materially change the failed execution contract when the original contract cannot satisfy the failure condition. |
| I-007 | Runtime Truth | UI state must be derived from authoritative runtime evidence rather than predicted domain state. |
| I-008 | Evidence | Consequential claims must be grounded in observable evidence whenever such evidence is available. |
| I-009 | Separation of Concerns | Intent, workspace, autonomy, artifact, execution strategy, mutation, and verification remain distinct concepts even when implemented by adjacent components. |
| I-010 | No Ghost Authority | No secondary or legacy execution path may silently bypass the authoritative runtime boundary. |

## 16. Relationship to Implementation

The Constitution defines what must remain true. It does not prescribe a particular implementation.

```
Constitution
    ≠
Architecture Diagram
    ≠
Implementation
    ≠
Runtime Behavior
```

Implementation must conform to the Constitution. Runtime behavior must provide evidence of that conformance.

`IMPLEMENTATION_AUDIT.md` exists to establish whether the implementation actually satisfies these constitutional requirements.

A divergence between implementation and Constitution must be recorded **before** it is corrected, unless immediate remediation is required for safety or integrity.

## 17. Final Principle

Izen should remain a system in which:

> Humans define intent, models provide reasoning, the runtime enforces authority, execution produces evidence, and verification establishes truth.

No individual component should be allowed to silently collapse these responsibilities into itself.

That separation is not incidental architecture. It is the constitutional identity of Izen.
