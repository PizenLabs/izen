# Izen Philosophy

> AI should strengthen human judgment, not replace it.

This is the constitutional foundation of Izen.

Every feature, workflow, runtime behavior, and architectural decision must preserve this principle.

If a feature increases automation but reduces human understanding, it must be rejected.

Izen is not an autonomous coding system.

Izen is a constrained cognitive runtime built to make the human stronger.

---

## Vision

Izen exists to build a better relationship between humans and intelligence.

Not faster by default.
Not smarter by illusion.
Not autonomous by trend.

But:

- clearer
- safer
- more inspectable
- more reversible
- more structurally intelligent

The long-term vision of Izen is to become:

> A local-first industrial cognition layer for engineering work.

A workspace where:

- structure is preferred over noise
- understanding is preferred over guessing
- plans are preferred over blind execution
- reversibility is enforced
- trust is earned through visibility

Izen is not designed for hype cycles.

It is designed for durability.

---

## Core Principles

### 1. Human-Centered

The human remains the source of truth.

AI is an assistant.

Not an authority.
Not an owner.
Not an autonomous operator.

Izen exists to help humans:

- understand code
- inspect systems
- plan changes
- investigate failures
- execute safely

Human judgment remains final.

**Rule:** Never optimize away human understanding.

### 2. Clarity Over Speed

Fast is useful.

Clear is required.

A fast answer that hides reasoning is dangerous.

Izen optimizes for:

> Clarity > Control > Trust > Speed

**Rule:** Unclear speed is technical debt.

### 3. Explicit Over Implicit

Nothing important happens silently.

The user must always know:

- what Izen is doing
- why it is doing it
- what retrieval path is active
- what fallback was triggered
- what failed

Examples:

```
[System] Graph lookup failed. Escalating to semantic search.
[System] Semantic confidence low. Fallback: ripgrep lookup.
```

**Rule:** Invisible behavior reduces trust.

### 4. Minimal by Default

Complexity must be proportional to intent.

Simple input must remain simple.

Saying:

```
Hi
```

must never trigger:

- graph indexing
- semantic retrieval
- execution engines
- repository scans

Context is both cost and debt.

Every retrieval step must estimate token weight before model submission.

**Rule:** Complexity must be intentional.

### 5. Local-First

Local is the default.

Not cloud.
Not remote.
Not external.

Izen assumes the machine running it owns the work.

Benefits:

- privacy
- speed
- transparency
- lower failure surfaces

Remote systems remain optional.

**Rule:** Remote is additive, never foundational.

### 6. Security-Aware & Scoped Isolation

Every capability carries risk.

Nothing should be trusted by default.

Especially:

- shell execution
- MCP servers
- file mutation
- external APIs
- infrastructure commands

Capabilities must be bounded.

**Rule:** Capability without boundaries becomes liability.

### 7. Reversible by Design

Mutation must always have a return path.

This includes:

- file changes
- patch application
- build outputs
- workspace plans
- shell-triggered state changes

Enforced by:

- Git checkpoints
- patch storage
- audit logs

**Rule:** If it cannot be reversed, it must not be automated.

### 8. Structure Before Intelligence

Raw context is expensive.

Structured context is leverage.

Izen prefers:

> Graph AST → Symbol Definitions → Call Chains → Dependency Slices

before:

- raw files
- full folders
- repository dumps

Compression precedes reasoning.

**Rule:** Compress context before intelligence.

### 9. Semantic-First, Text-Resilient

Retrieval order:

> Graph AST Query → Semantic Search → Text Fallback

Structure first.

Meaning second.

Text fallback always available.

Real-world repositories are messy.

Purity without fallback is fragility.

**Rule:** Purity without resilience is fragile.

### 10. Modes Over Prompts

Izen is mode-driven.

Never personality-driven.

Modes define:

- permissions
- runtime behavior
- retrieval boundaries
- tool access

Prompts seed intelligence.

Modes enforce behavior.

**Rule:** Behavior must be deterministic.

### 11. UI as a High-Throughput Dashboard

The terminal is an industrial workspace.

Not a casual chat app.

Persistent metadata belongs in fixed regions:

- status bars
- side panes
- runtime indicators

Scrolling is for:

- reasoning
- human dialogue
- active analysis

Never for critical metadata.

**Rule:** Metadata must remain visible.

### 12. Failure Is First-Class

Failure is expected.

Not exceptional.

Examples:

- graph failures
- parser failures
- semantic misses
- provider outages
- shell crashes

Failures must be:

- surfaced
- logged
- inspectable
- resumable

**Rule:** Hidden failure is worse than failed execution.

---

## Mode System

Modes define operational boundaries.

Not personalities.

A new mode may only exist if it creates a fundamentally new permission boundary.

### Capability Matrix

| Mode        | Read | Write | Shell | Test | Patch | Checkpoint |
|-------------|------|-------|-------|------|-------|------------|
| ask         | Yes  | No    | No    | No   | No    | No         |
| plan        | Yes  | No    | No    | No   | No    | Optional   |
| build       | Yes  | Yes   | Yes   | Yes  | Yes   | Required   |
| investigate | Yes  | No    | Yes   | Yes  | No    | Optional   |
| review      | Yes  | No    | No    | No   | No    | No         |

**Rule:** No new mode unless it introduces a fundamentally new permission boundary.

---

## Operational Rules

Before building anything:

1. Does it improve human understanding?
2. Does it reduce noise?
3. Does it preserve human control?
4. Does it increase trust?
5. Does it fit local-first?
6. Can Graph solve this first?
7. Is this context necessary right now?
8. Can this mutation be reversed?
9. Can local solve this before external?
10. Does this increase autonomy beyond understanding?
11. Can raw diagnostics be pre-filtered locally?
12. Does this exceed token budget?

If any answer violates the philosophy:

> do not build it.

---

## Anti-Patterns

### Blind Autonomy

**Wrong:** AI decides and executes without approval.

**Right:** AI proposes. Human decides.

### Context Dumping

**Wrong:** Dumping full repositories.

**Right:** Injecting compressed slices.

### Silent Fallbacks

**Wrong:** Fallbacks without visibility.

**Right:** Fallbacks are logged.

### Prompt Blob Architecture

**Wrong:** System behavior hidden in giant prompts.

**Right:** System boundaries enforced in code. Prompts only seed intelligence.

### Hidden Memory

**Wrong:** Invisible state influencing behavior.

**Right:** Memory must be inspectable.

### Feature Accumulation

**Wrong:** Building because competitors have it.

**Right:** Build only if philosophy allows it.

### The Omniscient Graph Trap

**Wrong:** Assuming graph truth is perfect.

**Right:** Graph is a map, not reality. Dirty workspaces require diff cross-reference.

---

## Command Registry

Minimal surface.

High semantic density.

Low noise.

### Commands

| Command | Description |
|---|---|
| `/help` | Reveal system capabilities. |
| `/quit` | Explicit exit. Persist session safely. |
| `/mode <ask\|plan\|build\|investigate\|review>` | Defines runtime boundary. Primary control surface. |
| `/objective` | Declares human intent. Focuses retrieval and execution. |
| `/clear` | Clears viewport. Resets cognitive noise. |
| `/drop [target]` | Removes context manually. Preserves human control. |
| `/undo` | Reverses local mutations. |
| `/commit` | Marks a verified safe baseline. |
| `/checkpoint` | Creates a pre-emptive rollback anchor. |

### Architectural Map Engine (`/arch`)

The `/arch` command strictly enforces the **Structure Before Intelligence** principle. It is a visual Trust Engine, preventing AI from guessing about the directory structure.

**Operating Rules:**

1. **Graph-First:** The Go engine must pre-construct a list of nodes (File/Symbol) and edges (Import/Call) from the Graph AST.
2. **Strict Boundary:** If the repository is too large, `/arch` automatically limits the display depth (max depth = 3) to avoid memory overflow for both the user and the SLM.
3. **Format Invariant:** The output must be normalized as a tree structure or a text-block diagram (Mermaid/Text-Block Diagram) for fixed display on the TUI; cumbersome dialog text is not allowed.

### Shell Escape Hatch

```
!<command>
```

Direct system access.

Bypasses AI mediation.

Must obey:

- read-only guards
- non-Git mutation guards
- explicit warnings

---

## Runtime Architecture

**Global:** `~/.izen/`

Contains:

- runtime
- lx engine
- config
- shared cache

**Local:** `./.izen/`

Contains:

- session
- graph
- patches
- checkpoints
- history
- audit

**Rule:** Executables are global. Knowledge is local.

---

## Final Law

If a feature makes Izen more powerful but less understandable:

> Do not build it.

If a feature makes Izen faster but less trustworthy:

> Do not build it.

If a feature makes Izen more autonomous but less human-centered:

> Do not build it.

If a feature violates reversibility, visibility, or explicit control:

> Do not build it.

Izen is not built to replace the human.

It is built to strengthen human judgment.
