# UI Design Philosophy

> Human-centered engineering intelligence.

The Izen interface is **not a markdown viewer**.

It is a structured engineering workspace where every visual element exists to
improve human understanding, review quality, and decision making.

Models generate information.

**Izen decides how that information is presented.**

The renderer is responsible for transforming model output into a consistent,
reviewable, engineering-focused interface.

---

# Design Principles

## Human judgment first

The interface should never encourage blind acceptance.

Every meaningful mutation should answer four questions before execution.

- What is changing?
- Why is it changing?
- Where is it changing?
- What are the risks?

The user should always feel like a reviewer rather than an operator approving AI.

---

## Renderer over Markdown

Markdown is an interchange format.

It is **not** the user interface.

Whenever information can be represented by a richer semantic component,
Izen should replace markdown rendering with a dedicated widget.

For example

Instead of

```markdown
## Plan

1. Analyze
2. Refactor
3. Test
```

Render

```
Plan

✓ Analyze graph

● Refactor middleware

○ Run tests
```

The renderer owns presentation.

The model only owns meaning.

---

## Structured over Decorative

Visual elements exist only to increase comprehension.

Do not add decorative UI.

No unnecessary gradients.

No animations without semantic meaning.

No avatars.

No "AI thinking..." gimmicks.

Every component should help the user review information faster.

---

## Information Hierarchy

The interface consists of five independent layers.

```
Conversation

↓

Engineering Widgets

↓

Input

↓

Status

↓

Footer
```

Every layer has a single responsibility.

No layer should duplicate another.

---

# Layout

The screen is divided into five regions.

```
──────────────────────────────────────────

Conversation Timeline

──────────────────────────────────────────

Engineering Widgets

──────────────────────────────────────────

Input

──────────────────────────────────────────

Runtime Status

──────────────────────────────────────────

Footer
```

---

# Conversation Timeline

The conversation is a chronological event log.

Its purpose is context.

Not presentation.

Conversation messages should remain lightweight.

Example

```
User

/build implement auth middleware

────────────────────────

IZEN

Analyzing dependency graph...

────────────────────────

IZEN

Proposal ready.
```

Large objects should never be embedded directly inside message text.

Instead, messages attach widgets.

```
Message

↓

Proposal Widget
```

This keeps the timeline readable regardless of proposal size.

---

# Engineering Widgets

Widgets are the primary UI abstraction.

They replace markdown whenever structured information exists.

Widgets are first-class UI components.

Examples

- Plan
- Diff
- Proposal
- Table
- Command
- Warning
- Success
- Tree
- Progress
- Evidence
- Risk
- File Summary
- Checkpoint

Every widget shares the same visual language.

```
╭─────────────────────────────╮

Title

─────────────────────────────

Content

╰─────────────────────────────╯
```

Users should immediately recognize widgets regardless of content.

---

# Widget Registry

The renderer maps semantic objects into widgets.

```
Model Output

↓

Renderer

↓

Widget
```

Examples

Plan

↓

Plan Widget

Table

↓

Table Widget

Diff

↓

Diff Widget

Command

↓

Command Widget

Risk Analysis

↓

Risk Widget

File List

↓

Tree Widget

Progress

↓

Progress Widget

---

# Proposal Widget

The Proposal Widget replaces plain "Apply changes?" dialogs.

Every proposal should explain intent before asking permission.

Structure

```
Proposal

Summary

Reason

Files

Risk

Expected Outcome

────────────────────────

Apply?
```

The interface should encourage informed approval.

Not blind confirmation.

---

# Diff Widget

Diffs should optimize review quality rather than raw git compatibility.

Every diff begins with location metadata.

Example

```
Edit

internal/ui/view.go

func getGreeting()

Lines 218-229

────────────────────────
```

Then the diff.

```
218 - return "Good morning"

222 + return fmt.Sprintf(...)
```

The widget should additionally display

- affected symbol
- file path
- line range
- change statistics
- risk level

Whenever available.

Multiple modified files should appear as collapsible sections.

```
Files

▶ internal/ui/view.go

▶ internal/core/parser.go

▶ README.md
```

The renderer should never dump dozens of git hunks into the conversation.

---

# Plan Widget

Plans represent engineering workflows.

Not markdown lists.

State machine

Pending

↓

Running

↓

Completed

↓

Failed

Example

```
Plan

✓ Analyze graph

✓ Build dependency slice

● Modify middleware

○ Execute tests

○ Review changes
```

The active step should always be visible.

---

# Table Widget

Markdown tables should render as terminal-native tables.

Example

```
┌────────────┬──────────┐

Service

Status

Port

...
```

Tables should support

- wrapping
- alignment
- resizing
- scrolling

without exposing markdown syntax.

---

# Evidence Widget

One unique capability of Izen.

Every important answer should expose where confidence originates.

Example

```
Evidence

Source

Graph Symbol Match

Confidence

96%

Fallback

Not used
```

Or

```
Evidence

Source

Semantic Search (Lynx)

Confidence

81%

Fallback

grep
```

This reflects

Explicit over Implicit.

---

# Risk Widget

Whenever mutations occur,
the renderer should summarize engineering risk.

Example

```
Risk

Scope

Single package

Breaking API

No

Tests

Passed

Rollback

Available
```

---

# Input

The input area remains permanently visible.

Its appearance changes with mode.

Example

Ask

```
ask ❯
```

Plan

```
plan ❯
```

Build

```
build ❯
```

Review

```
review ❯
```

Investigate

```
investigate ❯
```

Each mode owns a distinct accent color.

Users should identify the current mode without reading labels.

---

# Runtime Status

Runtime information describes execution.

Not conversation.

Examples

Current Model

Sandbox

Token Usage

Running Task

Checkpoint

Execution State

This region should update independently from conversation rendering.

---

# Footer

The footer displays stable environment metadata.

Suggested groups

Workspace

```
Project

Branch
```

Runtime

```
Model

Context

Tokens
```

Execution

```
Sandbox

Safe

Checkpoint
```

Footer data should never compete visually with engineering content.

---

# Spacing

Whitespace is a design tool.

Every section should follow consistent vertical rhythm.

Recommended

Message

(blank line)

Widget

(blank line)

Message

(blank line)

Widget

Avoid dense walls of text.

The interface should feel calm even during long sessions.

---

# Color Philosophy

Colors communicate state.

Not decoration.

Three categories exist.

Mode Colors

Used only for mode identity.

Semantic Colors

Success

Warning

Danger

Neutral Colors

Borders

Text

Metadata

No additional accent colors should be introduced without semantic meaning.

---

# Animation

Animation should communicate state transitions.

Examples

Proposal appears

Plan progresses

Task completes

Animation must never exist purely for aesthetics.

Keep durations below 200ms.

---

# Accessibility

Every widget should remain understandable in monochrome terminals.

Color should reinforce meaning.

Never define meaning exclusively through color.

Icons, labels, and structure must remain sufficient.

---

# Future Extensions

The renderer is intentionally component-driven.

Future widgets may include

- Architecture Graph
- Call Chain Viewer
- Dependency Slice
- Symbol Inspector
- Test Timeline
- Coverage Report
- MCP Issue Viewer
- Git History
- AST Viewer
- Mermaid Renderer

No architectural changes should be required to introduce new widgets.

The renderer should only need to register another component.

---

# Core Philosophy

The interface should never feel like chatting with an AI.

It should feel like reviewing engineering evidence.

Models generate content.

Izen generates understanding.

