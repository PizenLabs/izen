# Izen Architecture RFC v1.0

**Title:** Izen System Architecture - Refactored Specification  
**Version:** 1.0  
**Status:** Approved for Implementation  
**Date:** August 02, 2026  
**Goal:** Create a clean, maintainable, and evolvable architecture for Izen - a command-driven AI coding agent.

## 1. Guiding Principles

1. **Single Entry Point**  
   The `Runtime` is the only entry point of the system. No component may bypass it.

2. **Dependencies Flow Inward**  
   Outer layers may depend on inner layers, but never the reverse (Clean Architecture).

3. **Command In - Event Out**  
   All user interactions are expressed as commands. All state changes are communicated via events.

4. **Domain-Centric Design**  
   The core intelligence of Izen lies in the Workflow and its policies, not in UI or infrastructure.

5. **Explicit over Implicit + YAGNI**  
   Only introduce abstractions when they solve a real, current problem.

6. **Boundary Rules**  
   Architecture boundaries must be enforceable through code structure and imports.

7. **Evolution-First Design**  
   Design so that future changes do not break the core, rather than trying to support every possible future.

## 2. Layered Architecture

| Layer           | Responsibility                              | May Depend On          | Must Not Depend On       |
|-----------------|---------------------------------------------|------------------------|--------------------------|
| **Presentation** | User interface (BubbleTea, CLI, Web, etc.) | Application            | Domain, Infrastructure   |
| **Application**  | Orchestration, command handling, projections | Domain                 | Infrastructure           |
| **Domain**       | Business logic, workflow intelligence       | (none)                 | Any outer layer          |
| **Infrastructure**| External systems and adapters              | Domain interfaces      | Domain logic             |

### Detailed Components

**Presentation Layer**
- Pure view layer
- Emits `RuntimeCommand`
- Listens to Presentation Events

**Application Layer**
- `Runtime` (Facade - very thin)
- `CommandDispatcher`
- Command Handlers (one per major command)
- `LedgerBuilder` (builds context projection from domain events)

**Domain Layer**
- `WorkflowRuntime` (main orchestrator)
- `Phase` (Ask, Investigate, Plan, Build, Review)
- `TaskGraph` + `ExecutionCursor`
- `Policy` package (`SafetyPolicy`, `ApprovalPolicy`, `TransitionPolicy`, `ExecutionPolicy`)
- Ports: `CapabilityPort` interfaces (`PatchPort`, `ShellPort`, `GitPort`, `FilePort`, etc.)

**Infrastructure Layer**
- Concrete implementations of Capability Ports
- Event Publisher (in-memory channel initially)
- LLM clients
- Filesystem, Git, Shell executors

## 3. Core Concepts

### Runtime Flow
```
Runtime → CommandDispatcher → Specific Command Handler
                                 ↓
                         Domain (WorkflowRuntime)
                                 ↓
                         Domain Events
                                 ↓
                       LedgerBuilder + Event Translator
                                 ↓
                           Presentation Events
```

### Important Design Decisions

- **Runtime** is extremely thin: only routing and lifecycle management.
- **CommandDispatcher** maps commands to handlers.
- **Domain Events** vs **Presentation Events**: Domain must not emit UI-specific events. Use an Event Translator in Application layer.
- **Context Ledger** is a projection built by `LedgerBuilder`, not part of Workflow state.
- **Capabilities** are exposed as Ports (interfaces) in Domain. Concrete executors live in Infrastructure.
- **TaskGraph** starts as linear but is designed to evolve into DAG without breaking Cursor API.

## 4. Boundary Rules

- Presentation may only import Application.
- Application may only import Domain interfaces/ports.
- Domain may only depend on its own interfaces (no infrastructure).
- Infrastructure implements Domain ports but never calls upward.
- No direct imports from outer to inner layers.

## 5. Evolution Rules

**Designed to evolve easily:**
- TaskGraph (Linear → DAG)
- Event Publisher (in-memory → external)
- LLM providers
- UI frameworks
- Storage solutions

**Architectural Invariants (must never break):**
- Single Entry Point via Runtime
- Command → Handler → Domain → Event flow
- Dependencies flow inward
- Domain must not know about concrete infrastructure or UI

## 6. Architectural Decision Rules

When adding new code, ask:
1. Does it contain workflow/business logic? → Domain
2. Does it coordinate or translate? → Application
3. Is it UI rendering or input? → Presentation
4. Is it an external system adapter? → Infrastructure

## 7. Package Structure (Proposed)

```bash
internal/
├── runtime/           # Application Layer
│   ├── runtime.go
│   ├── dispatcher.go
│   └── handlers/
├── domain/            # Domain Layer
│   ├── workflow/
│   ├── policy/
│   ├── task/
│   └── ports/
├── infrastructure/    # Infrastructure Layer
│   ├── capabilities/
│   ├── events/
│   └── llm/
├── presentation/      # UI Layer
└── event/             # Shared event definitions
```

## 8. Next Steps (Implementation Phases)

**Phase 1:** Core Runtime + Commands + Dispatcher + Basic Handlers  
**Phase 2:** Domain (WorkflowRuntime, Cursor, Phase, Policies, Ports)  
**Phase 3:** Event System + LedgerBuilder + Infrastructure Adapters  
**Phase 4:** Integration + First Working Modes (/ask → /plan → /build)

---

**Approval**

This RFC v1.0 is now considered the architectural foundation of Izen.  
All future implementation and prompts to OpenCode (or other agents) **must** strictly follow this specification.

**Changes to this document after v1.0 will only be accepted based on real implementation experience.**
