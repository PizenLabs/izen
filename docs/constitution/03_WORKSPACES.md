# 03 — Workspace Boundaries & Contexts

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.1.0 |
| Last Reviewed | 2026-08-22 |

## Definition

A **Workspace** is the bounded physical or logical environment (e.g., Git repository, isolated directory, container) in which Izen operates.

A **Workspace Mode** is an interaction and operational context applied to a Workspace. Workspace Mode does **not** define physical isolation and does **not** grant execution authority.

## Workspace Modes

Workspace Modes determine context, **not** capability or execution surface:

| Mode | Primary Context | Default Capability Posture |
|---|---|---|
| `/ask` | Conversation & direct questions | Direct cognition; zero mutation |
| `/investigate` | Diagnosis, tracing, evidence gathering | Read / Analyze |
| `/plan` | Structural planning & architecture proposal | Read / Analyze / Propose |
| `/build` | Controlled workspace modification | Mutation requires explicit capability authorization |
| `/review` | Diff, verification, readiness evaluation | Read / Analyze / Verify |

## Separation of Concerns

- **Workspace** determines context.
- **Intent** determines objective.
- **Capability** determines permission.
- **Strategy** determines execution contract.
- **Artifact** determines proposed mutation structure.
- **Runtime** determines whether that artifact may become a mutation.

## Invariants

- **No Implicit Capabilities:** Workspace Mode must not imply Capability Grant.
- **No Strategy Dictation:** Workspace Mode must not determine Execution Strategy.
- **No Authorization Proxy:** Workspace Mode must not constitute Mutation Authorization.
- **Strict Scope Isolation:** All operations must remain scoped to the active Workspace boundary unless an explicit Runtime transition changes that boundary.
- **Tolerance of Input State:** Pre-existing uncommitted workspace state must be treated as input state, not as evidence of Runtime ownership or dirty state violations, unless explicit clean-state constraints are required by a specific execution contract.
