# Semantic UI Design

> Human-centered coding intelligence begins with human-readable interfaces.

---

# Purpose

The current UI successfully renders conversations, code blocks, tables, and diffs.

However, most mutation workflows still expose **raw implementation artifacts**
(file paths, unified diffs, line ranges) instead of the higher-level semantic
information already available inside IZEN.

This document defines how IZEN should present information using its existing
Graph, AST, Symbol Index and Semantic Retrieval infrastructure.

The objective is not to become prettier.

The objective is to become **clearer**.

---

# Design Philosophy

The UI must reflect the philosophy of IZEN.

| Philosophy | UI Implication |
|------------|----------------|
| Human-centered | Present intent before implementation. |
| Clarity over speed | Explain what changes before showing how. |
| Explicit over implicit | Every action exposes scope, impact and reasoning. |
| Structure before intelligence | Render symbols before files. |
| Semantic-first | Prefer AST concepts over raw text. |
| Local-first | No hidden operations. Everything visible. |
| Reversible | Every mutation displays rollback/checkpoint state. |

---

# Information Hierarchy

Never present implementation details first.

The hierarchy is:

Intent

↓

Target Symbol

↓

Impact

↓

Diff

↓

Execution

Never:

File

↓

Line numbers

↓

Git diff

↓

Hope the user understands.

---

# The Semantic Pyramid

Instead of:

File
↓

Text

↓

Diff

IZEN should render:

Workspace

↓

Module

↓

Symbol

↓

AST Node

↓

Text

The deeper the layer, the more implementation-specific.

The UI should stay as high as possible.

---

# Symbol-first Rendering

Whenever Tree-sitter identifies a symbol,
the UI should render the symbol instead of only the file.

Instead of:

File:
internal/ui/view.go

Lines:
220-241

Render:

Target

Function:
getGreeting()

Module:
internal/ui

Language:
Go

Only fall back to file/line when no symbol exists.

---

# Mutation Card

Every build proposal becomes one semantic object.

Instead of multiple disconnected widgets:

Diff

Proposal

Risk

Checkpoint

Render a single Mutation Card.

Example

╭────────────────────────────────────────────╮

Edit Proposal

Target

Function:
getGreeting()

Module:
internal/ui/view.go

Purpose

Personalize greeting using current username.

Impact

UI only

No public API changes

Risk

Low

────────────────────────────────────────────

Semantic Diff

(diff)

────────────────────────────────────────────

Checkpoint

cp-18274

Rollback available

────────────────────────────────────────────

[Accept]
[Reject]

╰────────────────────────────────────────────╯

Everything required to make a decision lives inside one card.

---

# Semantic Diff

Raw unified diff is implementation detail.

Whenever possible,
augment the diff using AST metadata.

Instead of:

@@ ...

Render:

Added

Field

Author

Removed

Parameter

config

Modified

Function

getGreeting()

Changed

Struct

Config

The raw patch remains available underneath.

Semantic summary first.

Implementation second.

---

# Symbol-aware Headers

Diff headers should never begin with file paths.

Preferred order:

Function

Method

Struct

Interface

Enum

Type

Variable

Constant

Module

File

Example

Function

BuildContext()

File

internal/context/build.go

Instead of

internal/context/build.go:198-241

---

# Semantic Reasoning

Every mutation should explain why.

Not generated from the LLM.

Generated from the proposal.

Example

Reason

Introduce username into greeting.

Instead of generic text like

Updated greeting.

Reason is mandatory.

---

# Impact Radius

Tree-sitter + Graph already know dependencies.

Surface that.

Example

Impact

✓ UI

✓ Greeting Banner

✓ Startup Screen

No API Changes

No Database Changes

No Public Symbols Changed

This is significantly more useful than line numbers.

---

# Dependency Awareness

Graph already knows callers.

Surface them.

Example

Referenced By

StartupBanner()

RenderHeader()

SessionInit()

Or

No downstream callers.

This builds confidence.

---

# Public API Detection

The UI should distinguish

Internal Mutation

Public API Mutation

Example

Scope

Internal

or

Scope

Public API

Breaking Change

This should immediately change visual emphasis.

---

# Risk Card

Risk should be computed.

Not hallucinated.

Example

Risk

LOW

Reason

Single function

No exported symbols

No dependency expansion

No tests affected

Instead of

Risk:
Low

without explanation.

---

# Execution Timeline

Instead of scattered status lines:

done

checkpoint

applied

Render a timeline.

Plan

✓

Generated

↓

Review

✓

Accepted

↓

Execution

✓

Applied

↓

Checkpoint

cp-18312

↓

Ready

This reflects the lifecycle.

---

# Conversation Flow

Conversation should read like a story.

User

↓

Assistant

↓

Mutation Card

↓

Execution Result

↓

Ready

Not

User

↓

Assistant

↓

Diff

↓

Status

↓

Metadata

↓

Prompt

---

# Progressive Disclosure

Do not overwhelm.

Default

Symbol

Purpose

Impact

Risk

Collapsed Diff

Expand only when requested.

The majority of edits should fit inside one screen.

---

# Diff Rendering Levels

Level 1

Semantic Summary

Added field

Removed function

Modified interface

Level 2

Syntax Highlighted Diff

Level 3

Full Unified Patch

Each level increases implementation detail.

---

# Retrieval Awareness

The UI should expose where information came from.

Example

Context Source

✓ Symbol Graph

✓ Semantic Search

✓ AST

or

Fallback

grep

This reinforces trust.

Nothing should appear magical.

---

# Tables

Structured information should always render as tables.

Examples

Execution Plan

| Step | Status | Notes |
|------|--------|------|
| Analyze | ✓ | Complete |
| Refactor | Pending | Waiting approval |

Dependency Summary

| Symbol | Type | Impact |
|---------|------|--------|

Risk Analysis

| Category | Status |
|-----------|--------|

Never fake markdown tables.

Render real tables.

---

# Code Blocks

Code blocks remain syntax-highlighted.

Long code blocks become scrollable.

Never consume the entire conversation.

Maximum default height:

15–20 lines

Expand on demand.

---

# Large Diffs

Large diffs should collapse automatically.

Example

12 files changed

Expand

LICENSE

README

internal/context

...

Selecting one opens the semantic diff.

Never dump hundreds of lines into the conversation.

---

# Footer

Footer should communicate current system state.

task

sandbox

mode

model

checkpoint

workspace

Avoid repeating information already visible elsewhere.

---

# What Makes IZEN Different

Claude Code is patch-centric.

Gemini CLI is file-centric.

IZEN should be symbol-centric.

The primary object is never the file.

The primary object is the program structure.

Graph

↓

Symbol

↓

AST

↓

Semantic Meaning

↓

Implementation

This is the defining UI characteristic of IZEN.

---

# Guiding Principle

The user should never need to mentally reconstruct
the architecture from a git diff.

IZEN already knows the program structure.

The UI should expose that knowledge.

The interface should explain software,
not merely display text.


