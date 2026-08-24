# 05 — Autonomy, Decision & Authorization

| Field | Value |
|---|---|
| Status | CONFIRMED |
| Version | 0.1.0 |
| Last Reviewed | 2026-08-22 |

## Architectural Principle

Autonomy in Izen is **not** a binary switch. It is a strictly bounded authorization boundary governed by Capability Grants, Risk Scopes, and Approval Constraints evaluated solely by the Runtime.

## Authorization Pipeline

```
Intent + Target Scope
        │
        ▼
Autonomy Decision (Policy Evaluation)
        │
   ┌────┴─────┐
   ▼          ▼
[Granted]  [Ask User] ──► Human Interaction ──► [Approve / Reject]
   │          │                                       │
   └──────────┼───────────────────────────────────────┘
              ▼
     Capability Grant (Issued to Runtime Host)
              │
              ▼
       Execution Phase
```

## Disambiguation of Autonomy Phasing

The following phases must remain strictly distinct:

- **Autonomy Decision:** The deterministic evaluation by Runtime policy of whether an operation requires specific capabilities in a scope, resulting in `granted`, `ask_user`, or `rejected`.
- **Autonomy Proposal:** The Agent's structured declaration of intended operations and requested capabilities. A proposal is an untrusted plan, **not** permission.
- **Authorization (Capability Grant):** The explicit issuance of execution authority to the Runtime Host for a defined capability and scope.
- **Execution:** The physical invocation of authorized side-effects by the Runtime Host.

## Invariants

- **Runtime Ownership of Grant:** A Capability Grant authorizes the Runtime Host to perform the corresponding class of operations within the granted scope. The grant does **not** confer execution authority upon the Agent. The Agent remains a proposal generator regardless of capability state.
- **Grant ≠ Mutation Approval:** A Capability Grant permits an operation class; it does **not** authorize arbitrary model output to become a workspace mutation. Every mutation must still pass the Artifact Contract before execution.
- **Non-Self-Elevation:** An Agent must not grant itself capabilities, expand its authorized file scope, or override policy constraints.
- **Explicit Lifespan:** Capability grants must have bounded scope and explicit lifespan (single-invocation or request-bound).
