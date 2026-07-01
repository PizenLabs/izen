# REVIEW_LAYOUT.md

> The purpose of review is to help humans make confident decisions, not consume metadata.

---

# Objective

The review experience should maximize confidence while minimizing cognitive load.

IZEN already understands software structure through:

- Tree-sitter
- Symbol Index
- Graph
- Semantic Search

The review interface should expose only the information required to make a decision.

Everything else should remain available through progressive disclosure.

---

# Design Philosophy

The review workflow follows IZEN's core principles.

| Philosophy | Review Implication |
|------------|--------------------|
| Human-centered | Keep evidence visible while making decisions. |
| Clarity over speed | Prioritize understanding over information density. |
| Explicit over implicit | Show decision context without overwhelming the user. |
| Minimal by default | Present only essential metadata. |
| Structure before intelligence | Identify the mutation by symbol before implementation. |
| Semantic-first | Prefer semantic context over raw implementation details. |

---

# Decision-first Workflow

Review should follow this sequence.

```
Understand Target

↓

Inspect Evidence

↓

Evaluate Risk

↓

Accept or Reject
```

Not

```
Read Metadata

↓

Scroll

↓

Find Diff

↓

Remember Changes

↓

Make Decision
```

The interface should reduce memory load.

The user should never need to remember what was changed while searching for the review controls.

---

# Information Hierarchy

The review interface prioritizes information in the following order.

```
Target Symbol

↓

Evidence

↓

Risk

↓

Decision

↓

Supporting Metadata
```

Implementation details remain available but should not dominate the screen.

---

# Review Layout

The review workspace is composed of three areas.

```
Conversation

↓

Evidence View

↓

Persistent Review Panel
```

Where:

- Conversation provides context.
- Evidence View displays the proposed mutation.
- Persistent Review Panel provides decision controls.

---

# Evidence View

The Evidence View is the primary object during review.

Evidence may include:

- Semantic Diff
- Syntax-highlighted Diff
- Symbol Summary
- Structural Changes

The Evidence View should occupy the majority of the available viewport.

The proposed change should remain visible while making decisions.

---

# Persistent Review Panel

The review panel remains visible throughout the review lifecycle.

Its implementation is intentionally unspecified.

Possible implementations include:

- Bottom dock
- Footer
- Side panel
- Split view

The implementation may change.

The behavior must remain consistent.

---

# Review Panel Content

The panel contains only information required for decision making.

Required

- Target Symbol
- Scope
- Risk
- Checkpoint
- Decision Actions

Optional

- Public API badge
- Breaking Change badge

Hidden by default

- Purpose
- Long semantic explanations
- Dependency summaries
- Execution timeline
- Extended reasoning

Secondary information remains accessible through expansion.

---

# Example

```
──────────────────────────────────────────────

Target       getGreeting()

Scope        Internal

Risk         LOW

Checkpoint   cp-18312

[A] Accept    [L] Allow All    [R] Reject

──────────────────────────────────────────────
```

The panel should remain compact and predictable.

---

# Design Constraints

The review layout must remain deterministic.

The review panel must never grow based on:

- proposal size
- reasoning length
- semantic explanation
- number of files
- dependency count

Large metadata is always collapsed.

The panel maintains a constant height.

Recommended height:

6–8 terminal rows.

---

# Progressive Disclosure

Additional information is available on demand.

Default

- Target
- Scope
- Risk
- Checkpoint
- Actions

Expanded

- Purpose
- Semantic Reasoning
- Dependencies
- Call Graph
- Execution Timeline
- Full Proposal Metadata

The default experience should optimize for clarity.

---

# Semantic Independence

The review layout must not assume semantic infrastructure is always available.

Preferred rendering order

```
Symbol

↓

Module

↓

File

↓

Range
```

If semantic resolution fails, the layout remains identical.

Only the displayed metadata changes.

This preserves a stable review experience regardless of retrieval quality.

---

# Evidence Sources

Review decisions should be supported by visible evidence.

Evidence may originate from:

- Tree-sitter
- Symbol Index
- Graph
- Semantic Search
- Unified Diff

The interface should expose semantic truth before implementation details whenever possible.

---

# Renderer Responsibilities

The review renderer is responsible only for presentation.

Allowed

- Layout
- Formatting
- Alignment
- Highlighting
- Collapsing
- View composition

Forbidden

- Tree-sitter queries
- Graph traversal
- Semantic computation
- LLM parsing
- Business logic
- Execution logic

Semantic information must already exist before rendering begins.

---

# Viewport Allocation

The review interface should prioritize evidence.

Recommended allocation

| Area | Target |
|------|--------|
| Conversation | 10–15% |
| Evidence View | 65–75% |
| Persistent Review Panel | 10–20% |

The Evidence View should always remain the dominant visual element.

---

# Accessibility

Decision controls must remain visible at all times.

Users should never scroll to:

- Accept
- Reject
- Allow All

Keyboard shortcuts should always be discoverable.

Review should be fully operable without a mouse.

---

# Out of Scope

This document does not introduce:

- new widgets
- animations
- visual effects
- additional semantic analysis
- styling redesign

Its purpose is to improve review ergonomics by preserving visibility of the proposed change.

---

# Acceptance Criteria

- Review controls remain visible throughout the review session.
- Review panel height is fixed and deterministic.
- The review panel never expands with proposal metadata.
- The Evidence View occupies the majority of the viewport.
- Users can inspect changes and make decisions without scrolling away from the evidence.
- Semantic metadata is progressively disclosed rather than always expanded.
- The layout degrades gracefully when semantic resolution is unavailable.
- Rendering remains independent from semantic computation.
- No regression to existing review functionality.

---

# Guiding Principle

The primary object during review is not the proposal.

It is the evidence.

The interface should help users answer one question quickly and confidently:

> **"Do I understand this change well enough to accept it?"**
