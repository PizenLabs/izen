
# Izen Session Architecture

**Status:** Proposed
**Version:** 1.0
**Scope:** `/session`, `/new`, Session Lifecycle, Session Switching, Compaction, Project Context
**Principle:** Session-local continuity without project-wide context inflation

---

## 1. Purpose

Izen supports multiple independent working sessions inside the same project.

A session represents a bounded working context:

* what the user is currently trying to accomplish;
* the conversation and reasoning history relevant to that work;
* workflow state and direction;
* decisions and unresolved items;
* references to artifacts and execution evidence.

Sessions must be independently resumable and switchable without requiring the LLM to ingest the complete history of the project or the complete history of another session.

The architecture therefore separates:

1. **Project State**
2. **Session State**
3. **Project Knowledge**
4. **Compacted Context**
5. **Runtime State**
6. **Raw Historical Evidence**

The core invariant is:

> A session may depend on project context, but project state must not become a serialized copy of every session.

---

# 2. Design Goals

## 2.1 Primary Goals

* Create a new session without destroying project state.
* Switch between existing sessions deterministically.
* Resume a session without replaying its entire transcript to the LLM.
* Preserve raw history independently from compaction.
* Keep session context bounded.
* Allow useful knowledge to survive across sessions.
* Prevent project-wide context from becoming a monolithic summary.
* Preserve auditability and reversibility.
* Keep session management independent from execution authority.
* Support future multi-agent and multi-model workflows.

## 2.2 Non-Goals

Session management does not:

* execute mutations;
* decide whether a user request should execute;
* replace the RuntimeExecutor;
* own Git state;
* own the project graph;
* act as a general-purpose memory database;
* automatically inject all project knowledge into every LLM request.

---

# 3. Core Model

The fundamental model is:

```text
Project
│
├── Project State
│   ├── configuration
│   ├── graph
│   ├── workspace
│   └── project-level knowledge
│
├── Runtime State
│
├── Audit / Evidence
│
└── Sessions
    ├── Session A
    ├── Session B
    ├── Session C
    └── Session D ← current
```

A session is not a project.

A project is not a session.

A transcript is not a session.

A compacted context is not the raw transcript.

---

# 4. Session Definition

A Session is a persistent identity representing one coherent unit of work.

Conceptually:

```text
Session
├── Identity
├── Lifecycle
├── Goal
├── Direction
├── Conversation
├── Workflow State
├── Decisions
├── Unresolved Items
├── Artifact References
├── Checkpoint References
├── Context State
└── Runtime Binding
```

A session should answer:

> "If I return to this work later, what do I need to know to continue?"

It should not answer:

> "What has ever happened in this project?"

That second question belongs to project-level state and evidence.

---

# 5. `.izen/` Ownership Model

`.izen/` is the project-local Izen state root.

Current state:

```text
.izen/
├── audit/
│   └── events.ndjson
├── checkpoints/
│   └── session-start/
│       └── checkpoint.json
├── config.json
├── debug/
│   └── payload.log
├── graph/
├── graph.bin.zst
├── history/
│   └── input.log
├── patches/
└── runtime.meta
```

The architecture must not interpret `.izen/` as equivalent to "current session".

The directory contains multiple ownership domains.

---

# 6. State Ownership

## 6.1 Project-Owned State

Project-owned state survives `/new` and session switching.

Examples:

```text
.izen/config.json
.izen/graph/
.izen/graph.bin.zst
```

Project state describes the environment shared by sessions.

---

## 6.2 Session-Owned State

Session-owned state describes one working context.

Recommended logical structure:

```text
.izen/sessions/
└── <session-id>/
    ├── session.json
    ├── history/
    ├── context/
    │   ├── compact.json
    │   └── snapshots/
    ├── checkpoints/
    └── references/
```

The exact physical layout may evolve, but the ownership boundary must remain stable.

---

## 6.3 Cross-Session Evidence

Audit data should remain independent from session storage.

```text
.izen/audit/events.ndjson
```

Events should contain the relevant `session_id`.

Conceptually:

```json
{
  "session_id": "8f31a2",
  "event": "execution.completed"
}
```

This permits project-wide forensic reconstruction without copying audit logs into every session.

---

## 6.4 Diagnostic State

Debug information remains outside session semantics.

```text
.izen/debug/
```

Debug data must never become part of the model context by default.

---

# 7. Session Identity

Every session has a stable immutable ID.

Example:

```text
8f31a2
```

The ID is the canonical identity.

Human-readable metadata may include:

```text
title
created_at
updated_at
status
mode
goal
```

The title is mutable.

The ID is not.

---

# 8. Session Lifecycle

A session follows:

```text
                    ┌──────────────┐
                    │    CREATED   │
                    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
                    │    ACTIVE    │
                    └──────┬───────┘
                           │
             ┌─────────────┼─────────────┐
             │             │             │
             ▼             ▼             ▼
          switch         archive       delete*
             │
             ▼
          dormant

* delete is explicit and irreversible at the
  session-management layer.
```

Switching sessions does not destroy the previous session.

It transitions the old session from:

```text
ACTIVE
```

to:

```text
DORMANT
```

and activates the target session.

---

# 9. `/new`

## 9.1 Semantic Definition

```text
/new
```

means:

> Create and activate a new session in the current project.

It does not mean:

* reset project;
* delete history;
* clear graph;
* reset configuration;
* recreate `.izen/`;
* erase audit evidence.

---

## 9.2 Lifecycle

```text
/new
  │
  ▼
Session Manager
  │
  ├── persist current session
  ├── finalize transient state
  ├── optionally update compact context
  │
  ▼
Create Session ID
  │
  ├── initialize session metadata
  ├── bind current workspace/project
  ├── initialize context state
  └── create session-start checkpoint
  │
  ▼
Activate New Session
```

The new session starts with:

```text
fresh conversation
fresh session workflow state
fresh session identity
```

while retaining access to:

```text
project configuration
project graph
workspace
Git state
project-level knowledge
```

subject to Context Compiler selection.

---

# 10. `/session`

`/session` is the Session Control Surface.

Primary responsibilities:

```text
/session
├── list
├── resume
├── inspect
├── rename
├── clone
├── archive
├── delete
└── compact
```

Suggested command contract:

```text
/session
/session list
/session resume <id>
/session inspect <id>
/session rename <id> <name>
/session clone <id>
/session archive <id>
/session delete <id>
/session compact <id>
```

The default `/session` command should present the current session and available sessions rather than dumping raw conversation history.

---

# 11. Session Switching

Session switching must be explicit and deterministic.

```text
/session resume <id>
```

Conceptually:

```text
Current Session
      │
      ├── persist
      └── detach
            │
            ▼
       Session Manager
            │
            ▼
       Load Target Session
            │
            ├── session metadata
            ├── compact context
            ├── recent context
            ├── workflow state
            └── relevant references
            │
            ▼
       Activate Target
```

Switching does not replay the complete transcript.

---

# 12. Session Context

A session contains three distinct context layers.

```text
Session
│
├── Raw History
│
├── Compacted Context
│
└── Recent Context
```

## 12.1 Raw History

The complete conversation/evidence history.

Properties:

* durable;
* append-oriented;
* recoverable;
* not sent wholesale to the LLM.

Raw history is the source of truth for conversation reconstruction.

---

## 12.2 Compacted Context

A derived representation of the session.

It should capture:

```text
Goal
Direction
Important Decisions
Completed Work
Current Work
Unresolved Items
Constraints
Important Artifacts
Relevant References
```

Compaction is derived state.

It must never replace raw history.

---

## 12.3 Recent Context

Recent turns remain available because they contain information that may not yet justify another compaction cycle.

The Context Compiler combines:

```text
Compacted Context
+
Recent Turns
+
Relevant Project Knowledge
+
Relevant Workspace/Artifact Context
```

rather than sending the entire session history.

---

# 13. Compaction

Compaction exists to solve context economics.

The goal is not:

> Summarize everything.

The goal is:

> Preserve the minimum sufficient state required to continue the work correctly.

---

## 13.1 Session Compaction

Session compaction answers:

> What must this session remember to continue correctly?

Example:

```json
{
  "session_id": "8f31a2",
  "goal": "Redesign session lifecycle",
  "direction": "Separate session state from project state",
  "decisions": [
    "RuntimeExecutor remains mutation authority"
  ],
  "completed": [
    "Defined session/project boundary"
  ],
  "in_progress": [
    "Implement Session Manager"
  ],
  "unresolved": [
    "Finalize physical persistence layout"
  ],
  "artifacts": [
    "docs/architecture/SESSION.md"
  ]
}
```

The exact schema may evolve, but the semantic categories should remain structured.

---

# 14. Compaction Must Be Non-Destructive

The following invariant is mandatory:

```text
Raw History
    │
    ▼
 Compactor
    │
    ▼
Compacted Context
```

Never:

```text
Raw History
    │
    ▼
Compactor
    │
    └── DELETE RAW HISTORY
```

If a compaction is incorrect:

```text
delete compact
      │
      ▼
rebuild from raw history
```

This preserves reversibility and auditability.

---

# 15. Project-Level Context

Project context is different from session context.

Project context represents knowledge that should survive session boundaries.

Examples:

```text
Architecture decisions
Project conventions
Stable constraints
Important discoveries
Long-lived design decisions
Cross-session facts
```

It should not be a summary of every conversation.

---

# 16. Project Knowledge Consolidation

The project-level operation should conceptually be:

```text
Session A ─┐
Session B ─┤
Session C ─┼──► Knowledge Extraction
Session D ─┘
                   │
                   ▼
          Project Knowledge
```

A session may produce a project-level decision.

Example:

```text
Session A
    │
    │ discovers
    ▼
"RuntimeExecutor is the sole mutation authority"
    │
    │ promote
    ▼
Project Knowledge
```

Another session can then use that knowledge without loading Session A.

---

# 17. Do Not Create a Monolithic Project Summary

The following architecture is prohibited:

```text
All Sessions
     │
     ▼
One Giant Summary
     │
     ▼
Every LLM Request
```

This recreates the original context-window problem at the project level.

Instead:

```text
Project Knowledge
      │
      ▼
Relevant Retrieval
      │
      ▼
Context Compiler
```

Only relevant knowledge enters the model context.

---

# 18. Context Compilation

Context compilation is the final step before model invocation.

Conceptually:

```text
                    Project State
                         │
                         ▼
                Relevant Retrieval
                         │
                         ▼
Session Compact ────────┐
Recent Turns ───────────┤
Workflow State ─────────┤
Artifacts ──────────────┤
Project Knowledge ──────┤
                         ▼
                 Context Compiler
                         │
                         ▼
                  Budget / Policy
                         │
                         ▼
                       LLM
```

The Context Compiler decides:

> What does the model need to see now?

Session management decides:

> What state belongs to this session?

Project knowledge decides:

> What knowledge survives sessions?

These are separate responsibilities.

---

# 19. Context Priority

When context budget is constrained, the default priority should be:

```text
1. Current user request
2. Active workflow state
3. Recent conversation
4. Session compact
5. Relevant artifacts
6. Relevant project knowledge
7. Older session context
```

The system should prefer **relevance over chronology**.

---

# 20. Session Switching + Context Compilation

When switching:

```text
/session resume B
```

Izen should not perform:

```text
load A transcript
load B transcript
load all project history
send everything to LLM
```

Instead:

```text
Session B
   │
   ├── compact context
   ├── recent turns
   ├── workflow state
   └── relevant references
            │
            ▼
     Project retrieval
            │
            ▼
     Context Compiler
            │
            ▼
           LLM
```

This makes switching cheap in both latency and token usage.

---

# 21. Runtime Binding

Session management does not own execution.

The relationship should be:

```text
Session
   │
   │ provides identity/context
   ▼
Runtime
```

not:

```text
Runtime
   │
   │ creates/manages
   ▼
Session
```

A runtime execution should carry:

```text
session_id
```

so execution evidence can be correlated with the originating session.

Example:

```text
Execution
├── execution_id
├── session_id
├── workflow
├── provider
├── model
├── usage
├── mutation evidence
└── result
```

---

# 22. Audit Correlation

Audit events should be session-aware.

```text
Project Audit Log
        │
        ├── session A
        ├── session A
        ├── session B
        ├── session C
        └── session B
```

This allows:

```text
session → executions → mutations → artifacts
```

to be reconstructed without duplicating evidence.

---

# 23. Checkpoints

Checkpoints are recovery state, not conversation history.

A checkpoint may reference:

```text
session_id
workspace state
workflow state
artifact state
runtime state
```

A session-start checkpoint should be associated with the session that created it.

Conceptually:

```text
Session
   │
   └── Checkpoints
        ├── session-start
        ├── workflow checkpoint
        └── recovery checkpoint
```

Checkpoint creation must not require replaying the entire conversation.

---

# 24. Clone Semantics

Cloning a session creates a new session identity while preserving selected logical state.

```text
Session A
   │
   │ clone
   ▼
Session B
```

B must have:

```text
new session_id
independent lifecycle
independent future history
```

while optionally inheriting:

```text
goal
direction
compact context
relevant references
workflow starting state
```

Clone must not create shared mutable session state.

---

# 25. Archive Semantics

Archive means:

```text
no longer active
still recoverable
```

An archived session should remain inspectable and resumable unless explicitly deleted.

---

# 26. Delete Semantics

Deletion is an explicit lifecycle operation.

Deletion must not silently remove:

```text
project configuration
project graph
project knowledge
global audit evidence
```

Session deletion should affect only session-owned state, subject to retention policy.

Audit evidence should remain available where required for forensic integrity.

---

# 27. Concurrency

Only one session should be the active interactive session for a given Izen process/workspace context.

Conceptually:

```text
Project
│
├── Session A   dormant
├── Session B   dormant
└── Session C   ACTIVE
```

Multiple sessions may exist concurrently in storage.

Only one is attached to the current interactive context.

Future multi-agent execution may reference multiple sessions, but that should not change the interactive session invariant.

---

# 28. Session State Machine

Recommended state machine:

```text
                create
                   │
                   ▼
               CREATED
                   │
                   ▼
                ACTIVE
                   │
        ┌──────────┼──────────┐
        │          │          │
      switch     archive    delete
        │          │          │
        ▼          ▼          ▼
     DORMANT    ARCHIVED    DELETED
        │
        │ resume
        └──────────────► ACTIVE
```

Only explicit lifecycle commands may cause destructive transitions.

---

# 29. Failure Semantics

Session operations must fail closed.

For `/new`:

```text
If current session cannot be persisted:
    do not silently discard it
    do not activate the new session
```

For `/session resume`:

```text
If target session cannot be loaded:
    preserve current session
    report failure
    do not partially switch
```

The system must avoid:

```text
CURRENT SESSION
      ↓
partially persisted
      ↓
target partially loaded
      ↓
corrupted active state
```

---

# 30. Atomic Session Switching

The conceptual transaction is:

```text
1. Validate target session
2. Persist current session
3. Prepare target context
4. Commit active-session change
5. Activate target
```

If any pre-commit operation fails:

```text
current session remains active
```

The switch must be atomic from the user's perspective.

---

# 31. `/new` and `/session` Boundary

The two commands have deliberately different responsibilities.

### `/new`

```text
Create
```

### `/session`

```text
Manage
```

Therefore:

```text
/new
    → create + activate

/session list
    → discover

/session resume
    → switch

/session inspect
    → inspect

/session compact
    → maintain context

/session archive
    → lifecycle

/session delete
    → destructive lifecycle
```

This keeps the command model predictable.

---

# 32. Context Economics Invariant

The system must guarantee:

```text
Project Size ↑
    ≠
LLM Input Tokens ↑ linearly
```

Likewise:

```text
Session History ↑
    ≠
LLM Input Tokens ↑ linearly
```

Instead:

```text
Project Size
      │
      ▼
Retrieval
      │
      ▼
Relevant Context

Session History
      │
      ▼
Compaction + Recent Context
      │
      ▼
Relevant Context
```

LLM input should scale primarily with **current task complexity**, not historical project size.

---

# 33. Recommended Logical Architecture

```text
                     ┌──────────────────────┐
                     │       Commands       │
                     │                      │
                     │ /new                 │
                     │ /session             │
                     └──────────┬───────────┘
                                │
                                ▼
                     ┌──────────────────────┐
                     │   Session Manager    │
                     │                      │
                     │ lifecycle            │
                     │ switching            │
                     │ persistence          │
                     └──────────┬───────────┘
                                │
              ┌─────────────────┼─────────────────┐
              │                 │                 │
              ▼                 ▼                 ▼
       Session Store      Context Manager    Checkpoint Store
              │                 │
              │                 ├── Compactor
              │                 ├── Project Knowledge
              │                 └── Context Compiler
              │
              ▼
        Session State
                                │
                                ▼
                         Runtime Context
                                │
                                ▼
                         RuntimeExecutor
                                │
                                ▼
                              LLM
```

The critical architectural direction is:

```text
Session Manager
       ≠
RuntimeExecutor
       ≠
Context Compiler
       ≠
Project Knowledge
```

---

# 34. Core Invariants

The implementation must preserve these invariants.

### INV-SESSION-01

Every session has a stable immutable ID.

### INV-SESSION-02

Only one interactive session is active at a time per workspace context.

### INV-SESSION-03

`/new` creates a new session and does not reset project state.

### INV-SESSION-04

Switching sessions does not require replaying the complete session transcript.

### INV-SESSION-05

Raw session history remains recoverable after compaction.

### INV-SESSION-06

Compaction is derived state and may be rebuilt.

### INV-SESSION-07

Project knowledge is not a monolithic summary of all sessions.

### INV-SESSION-08

Only relevant project knowledge enters model context.

### INV-SESSION-09

Session management cannot directly execute workspace mutations.

### INV-SESSION-10

Runtime executions are correlated with `session_id`.

### INV-SESSION-11

Failed session switching leaves the current session active.

### INV-SESSION-12

Session deletion cannot implicitly delete project state.

### INV-SESSION-13

Context size is controlled by policy/budget rather than raw history size.

---

# 35. Example End-to-End Flow

## Create

```text
/new
  ↓
Session Manager
  ↓
Create ID
  ↓
Initialize session
  ↓
Create checkpoint
  ↓
ACTIVE
```

## Work

```text
User
  ↓
Intent Gateway
  ↓
Workflow
  ↓
RuntimeExecutor
  ↓
Execution
  ↓
Audit
  ↓
Session State
```

## Compact

```text
Raw History
  ↓
Compactor
  ↓
Structured Session Context
```

## Switch

```text
/session resume B
  ↓
Persist A
  ↓
Load B
  ↓
Load B compact
  ↓
Load recent B context
  ↓
Retrieve relevant project knowledge
  ↓
Context Compiler
  ↓
LLM
```

## Promote Knowledge

```text
Session B
  ↓
Knowledge Extraction
  ↓
Project Knowledge
```

---

# 36. Architectural Principle

Izen should treat context as a compiled resource, not as a transcript dump.

The complete model is:

```text
                 RAW EVIDENCE
                      │
          ┌───────────┴───────────┐
          │                       │
      Session                 Project
      History                 Evidence
          │                       │
          ▼                       ▼
     Compaction              Knowledge
          │                       │
          └───────────┬───────────┘
                      ▼
               Context Compiler
                      │
                Budget / Policy
                      │
                      ▼
                     LLM
```

Therefore:

> **Sessions preserve continuity.**
>
> **Compaction preserves direction.**
>
> **Project knowledge preserves durable learning.**
>
> **Context compilation decides what the model actually sees.**

This separation is the foundation for scalable multi-session operation in Izen.
