# Izen Architectural Guardrails & Meta-Rules

> **Note for Contributors & AI Agents:** Izen enforces a strict **Meta-Architecture**. Any Pull Request that violates these guardrails will be rejected during code review or automated linting.

---

## Core Meta-Rules

### 1. Rule "One Question, One Owner"

Every architectural question in Izen must have **exactly one authoritative owner**. If two components can answer the same question, the architecture is drifting and the design must be corrected.

| Component | Question Owned (ONLY permitted to answer) | Forbidden Concepts (Must NOT own) |
| :--- | :--- | :--- |
| **`User Intent Model`** | *"What does the user want to achieve?"* | Execution tasks, patches, risk scores, or directives. |
| **`Capability Graph`** | *"What physical tools/facts does the machine possess?"* | Governance rules, `Allow/Deny` decisions, or mode checks. |
| **`Policy Engine`** | *"Is this specific action permitted under current governance?"* | OS probing, tool discovery, or execution re-planning. |
| **`Decision Engine`** | *"What directive should be dispatched next?"* | Direct token budget math, direct risk scoring, or direct patching. |
| **`Workflow StateMachine`** | *"What macro business phase is the system in?"* | Micro-level DAG node states or direct LLM prompt building. |
| **`Pipeline Engine`** | *"How to transform source code (Layers 0–4)?"* | Mode orchestration, business routing, or TUI presentation. |
| **`Execution Timeline`** | *"What happened chronologically across the system?"* | Code mutation logic, decision making, or approval parsing. |
| **`UI`** | *"What should the user see?"* (Pure Projection) | State mutation, direct tool calls, or fallback decisions. |

---

### 2. Strict Unidirectional Layering

Dependency strictly flows **DOWN**. Events strictly flow **UP**.

```
[ Top Layer ]        UI (Pure Projection)
                        │        ▲
                        ▼        │  (Domain Events / Envelopes flow UP)
                    Workflow StateMachine
                        │        ▲
                        ▼        │
                  Decision / Policy Engine
                        │        ▲
                        ▼        │
                   Pipeline Engine (Layers 0-4)
                        │        ▲
                        ▼        │
[ Bottom Layer ]  Runtime / Infrastructure / Domain
```

- **Imports:** Higher layers may import lower layers. Lower layers (`Runtime`, `Pipeline`, `Domain`) **MUST NEVER** import higher layers (`UI`, `Workflow`, `Decision`).
- **Communication:** Lower layers communicate state changes to higher layers exclusively via `internal/events.Bus` using `events.Envelope`.

---

### 3. Concrete Architectural Constraints

1. **Single Composition Root:** All application dependencies MUST be wired strictly inside `internal/runtime/compose/compose.go:Wire()`. Components (including `internal/ui`) must NEVER instantiate core engines directly via `new()` or package constructors.
2. **No Stringly-Typed Routing:** Internal failure classification, error recovery, and mode handoffs MUST use `internal/domain/signal.Signal`. Matching raw terminal strings or regex in routing logic is strictly prohibited.
3. **Thin Controllers:** The `DecisionEngine` is a thin controller. All specific policies (Budget, Retry, Risk) MUST be injected as interface strategies.
4. **Adapter-First Migration:** When updating core abstractions, build an Adapter first to wrap legacy channels/buses before removing old interfaces.
