# REPORT.md

# Engineering Workspace UI Finalization Report

Status: Final UI Polish Before Core Development

Version: 1.0

---

# Objective

Finalize the engineering workspace UX so it becomes stable enough to stop
iterating on presentation and return development focus to the core
intelligence stack.

This report intentionally excludes cosmetic improvements.

Only issues that directly affect clarity, review efficiency and human decision
making are included.

---

# Overall Assessment

Current Progress

█████████░ 90%

The interface already reflects the philosophy of IZEN:

✓ conversation-first

✓ diff-first

✓ semantic-oriented

✓ calm engineering aesthetic

✓ human review workflow

The remaining work is mostly viewport optimization.

---

# Remaining Issues

---

## 1. Compact Review Footer

Priority

★★★★★ Critical

Problem

The review footer still occupies too much vertical space.

Current

Target

Scope

Risk

Checkpoint

Actions

This consumes approximately 7–9 lines.

The diff loses valuable viewport height.

Goal

Maximum height:

4–5 lines

Preferred

--------------------------------------------------

LICENSE • Internal • LOW • cp-18312

[A] Accept   [L] Allow All   [R] Reject

--------------------------------------------------

Requirements

- fixed height

- sticky

- never scroll

- keyboard shortcuts always visible

Acceptance

Review footer never exceeds five lines.

---

## 2. Maximize Diff Viewport

Priority

★★★★★ Critical

Problem

Metadata still competes with the diff.

The diff is the evidence.

Everything else supports the evidence.

Target Layout

Conversation

↓

Diff (75–80%)

↓

Sticky Review Footer (20%)

Acceptance

The diff always occupies most of the screen.

---

## 3. Symbol-first Header

Priority

★★★★☆

Problem

Review headers still expose

File

Range

before semantic information.

Target

Function

Module

Language

Fallback

File

Range

Only when Tree-sitter cannot identify a symbol.

Acceptance

Files become fallback information rather than primary identity.

---

## 4. Remove Duplicate Metadata

Priority

★★★★☆

Problem

Information appears multiple times.

Examples

Edit

File

Target

LICENSE

All describe the same object.

Goal

Each piece of information should appear exactly once.

Acceptance

No duplicated identifiers inside review mode.

---

## 5. Compact Review Header

Priority

★★★☆☆

Problem

Review headers consume unnecessary vertical space.

Instead of

--------------------------------

Edit

--------------------------------

Use

Edit • LICENSE

or

Edit • getGreeting()

Acceptance

Header occupies one line.

---

## 6. Sticky Decision Bar

Priority

★★★★★ Critical

Problem

Decision controls belong to the viewport,
not the conversation.

Goal

Accept

Allow All

Reject

remain visible regardless of scrolling.

Acceptance

User never scrolls to locate review actions.

---

## 7. Semantic Summary Above Diff

Priority

★★★★☆

Problem

The user immediately sees raw diff.

Provide one concise semantic sentence first.

Example

Modified Function getGreeting()

No public API changes

1 file affected

Then show the patch.

Maximum

2 lines.

Acceptance

Summary never exceeds two lines.

---

## 8. Reduce Visual Noise

Priority

★★★☆☆

Problem

Excessive borders and separators reduce information density.

Goal

Only major widgets receive borders.

Internal sections should rely on spacing instead.

Acceptance

Cleaner engineering workspace.

---

## 9. Responsive Widget Height

Priority

★★★★☆

Problem

Small edits currently allocate the same UI space as large edits.

Goal

Widget height adapts to content.

Small diff

↓

Compact review

Large diff

↓

Diff expands naturally

Acceptance

No unnecessary whitespace.

---

## 10. Freeze UI

Priority

★★★★★ Required

After completing the above items

STOP

No additional redesign.

No animations.

No theme experiments.

No visual rewrites.

Future work should target

- Tree-sitter

- Graph Engine

- Symbol Index

- Semantic Search

- Context Builder

- Dependency Analysis

- Review Intelligence

- Investigate Engine

The UI should become a stable window into the engineering system,
not the engineering effort itself.

---

# Explicitly Out of Scope

The following ideas are intentionally excluded.

- animations

- gradients

- visual effects

- richer color palettes

- decorative widgets

- dashboard redesign

- chat bubbles

- avatars

- markdown styling improvements

These do not improve engineering productivity.

---

# Success Criteria

The UI is considered complete when the following conditions are true.

✓ Diff always dominates the viewport

✓ Review footer occupies ≤5 lines

✓ Decision controls are permanently visible

✓ Symbol information replaces file information whenever possible

✓ No duplicated metadata

✓ Review workflow requires zero scrolling to make a decision

✓ Small edits remain compact

✓ Large edits prioritize code visibility

✓ Interface reflects IZEN philosophy

---

# Final Principle

The purpose of the UI is not to impress.

The purpose of the UI is to shorten the distance between
human judgment and program understanding.

When the UI disappears from the user's attention,
the design is complete.

Development effort should then move permanently back to the core intelligence
of IZEN:

Graph → Symbol → AST → Semantic Understanding → Human Decision.
