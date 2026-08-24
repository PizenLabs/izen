# 04 — Intent Classification & Domain Boundaries

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.1.0 |
| Last Reviewed | 2026-08-22 |

## Definition

**Intent** is the formal interpretation of Human input. It declares the goal, target scope, and constraints of an operation before any agent reasoning or runtime execution occurs.

## Intent Taxonomy

| Intent Kind | Description | Permitted Default Actions |
|---|---|---|
| `conversation` | Free-form discussion, concept clarification. | None (cognition only) |
| `question` | Querying codebase or system behavior. | Read-only context retrieval |
| `investigation` | Diagnosing a bug, tracing execution paths. | Read-only tools, log inspection |
| `planning` | Generating implementation specifications. | Read-only, artifact creation (documents) |
| `modification` | Altering codebase, configuration, or state. | Triggers Runtime policy evaluation for mutation capabilities |
| `review` | Evaluating past mutations or external diffs. | Read-only, verification checks |

## Core Contracts & Questions

Intent explicitly answers: *"What does the Human want to achieve?"*

- **Intent vs. Authorization:** Intent classification never grants capability or side-effect authority. An intent of `modification` merely causes the Runtime to evaluate whether mutation capability is required and whether such capability may be granted under active policy. Intent itself grants no capability.
- **Target Scope Resolution:** Intent may declare a target scope. Target scope must be resolved and bounded by the Runtime before execution. An Agent must not expand the declared target scope implicitly, and a capability grant must not expand the Intent target scope.

## Invariants

- **Immutable Intent Identity:** An ExecutionRequest retains its resolved Intent ID throughout its lifecycle.
- **Identity Preservation in Recovery:** Recovery must not create a new Intent. Recovery may create a new execution attempt or execution contract while preserving original Intent identity.
- **Strict Escalation:** A `conversation` or `question` Intent must not quietly transition into workspace mutation without explicit Intent re-classification and Capability Grant.
