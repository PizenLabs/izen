# ASK_RENDERING.md

> Markdown is an interchange format. The user interface is semantic.

---

# Purpose

`/ask` is the primary interaction mode in IZEN.

Developers spend the majority of their time reading explanations,
investigating architecture, understanding code, and discussing ideas.

The current implementation renders Markdown almost literally.

Although technically correct, this creates visual noise and places the burden
of interpretation on the user.

IZEN should not display Markdown.

IZEN should understand Markdown and render its meaning.

The objective is to transform AI responses into an engineering-first reading
experience.

---

# Philosophy

This renderer follows IZEN's core principles.

| Philosophy | Rendering Implication |
|------------|-----------------------|
| Human-centered | Optimize for reading, not Markdown fidelity. |
| Clarity over speed | Information hierarchy is more important than raw formatting. |
| Explicit over implicit | Important concepts receive visual emphasis automatically. |
| Structure before intelligence | Render document structure before styling text. |
| Semantic-first | Render meaning, not syntax. |
| Minimal by default | Decoration exists only when it improves comprehension. |

---

# Rendering Pipeline

The rendering pipeline should become:

```

LLM Response

↓

Markdown Parser

↓

Markdown AST

↓

Semantic Render Tree

↓

Terminal Renderer

↓

Viewport

```

The renderer should never directly print raw Markdown.

Instead, every Markdown node becomes a semantic UI component.

---

# Renderer Architecture

```

MarkdownRenderer

├── HeadingRenderer
├── ParagraphRenderer
├── ListRenderer
├── QuoteRenderer
├── TableRenderer
├── CodeRenderer
├── InlineCodeRenderer
├── LinkRenderer
├── CalloutRenderer
├── HorizontalRuleRenderer
└── TaskListRenderer

```

Each renderer owns its layout.

No renderer should manually inspect raw Markdown text.

---

# Information Hierarchy

Reading order should always be obvious.

Priority:

```

Heading

↓

Summary

↓

Important Notes

↓

Lists

↓

Tables

↓

Code

↓

References

```

The renderer should naturally guide the eye.

---

# Headings

Markdown headings become semantic section titles.

Instead of rendering:

```

### Graph Traversal

```

Render

```

Graph Traversal
────────────────────────────

```

Rules

H1

- largest emphasis
- bold
- accent color

H2

- bold
- primary text

H3

- bold
- slightly muted accent

H4+

- minimal emphasis

Never display Markdown heading syntax.

---

# Paragraphs

Paragraphs should prioritize readability.

Rules

- wrap naturally
- consistent spacing
- blank line between paragraphs
- no unnecessary indentation

Large walls of text should never appear.

---

# Emphasis

Markdown emphasis becomes semantic emphasis.

Instead of

```

**important**

```

Render

bold text.

Instead of

```

*optional*

```

Render

muted emphasis.

Nested emphasis should remain readable.

---

# Lists

Bullets should be visually lightweight.

Instead of

```

- item
- item
- item

```

Render

```

• item

• item

• item

```

Ordered lists

```

1.

2.

3.

```

should preserve numbering.

Nested lists should increase indentation only.

---

# Task Lists

Markdown

```

- [ ] Pending
- [x] Complete

```

Render

```

○ Pending

✓ Complete

```

Task lists should use semantic colors.

---

# Quotes

Markdown

```

> Important

```

Render

```

┃ Important

```

One vertical accent line is sufficient.

Avoid boxed layouts.

---

# Callouts

The renderer should recognize common patterns.

Examples

IMPORTANT

NOTE

TIP

WARNING

CAUTION

These become semantic callouts.

Example

```

⚠ Warning

Tree-sitter failed.

Using grep fallback.

```

No Markdown syntax should remain visible.

---

# Code Blocks

Code is the most important element in `/ask`.

Code should never be surrounded by decorative boxes.

Instead

```

Python

def sort():

```

Rules

- syntax highlight
- preserve indentation
- optional language label
- no heavy borders
- full available width
- horizontal separator only if necessary

---

# Tree-sitter Highlighting

Whenever the language is supported,
Tree-sitter should replace regex highlighting.

Highlight

keywords

types

functions

methods

parameters

constants

comments

strings

numbers

The renderer should leverage IZEN's existing parsing infrastructure.

---

# Inline Code

Inline code should receive subtle emphasis.

Example

```

Tree-sitter

```

Use

- monospace
- slightly different background
- no excessive padding

---

# Tables

Markdown tables should become real tables.

Instead of

```

| Symbol | Type |

```

Render

```

Symbol           Type
────────────────────────────
BuildContext     Function
Config           Struct

```

Rules

- automatic column sizing
- left alignment
- optional right alignment for numbers
- no Markdown pipes

---

# Horizontal Rules

Markdown

```

---

```

becomes

```

────────────────────────────

```

Nothing more.

---

# Links

Links should prioritize readability.

Instead of

```

[text](...)

```

Render

```

text

```

Optional

Display destination on selection.

Avoid long URLs inside conversations.

---

# Images

When images cannot be displayed,
render placeholders.

Example

```

Image

architecture.png

```

Never dump raw Markdown image syntax.

---

# Long Code Blocks

Large snippets should remain readable.

Rules

Default height

15–20 lines

Collapsed

```

Go

128 lines

Press Enter to expand

```

The viewport should never be consumed by one code block.

---

# Large Tables

Large tables should support scrolling.

The conversation itself should remain readable.

---

# Color Hierarchy

Colors communicate importance.

Primary

Headings

Function names

Success

Secondary

Paragraphs

Muted

Metadata

Paths

Line numbers

Errors

Warnings

Information

Use color sparingly.

Never create a rainbow interface.

---

# Semantic Highlighting

The renderer should detect important concepts.

Examples

Function names

Types

Structs

Interfaces

Packages

Modules

File paths

Commands

Keyboard shortcuts

These should receive automatic semantic styling.

Example

```

BuildContext()

```

appears differently from

```

internal/context

```

---

# Progressive Disclosure

Not every detail belongs on screen immediately.

Default

Summary

Important sections

Collapsed code

Collapsed tables

Expanded on demand

---

# Rendering Goals

A developer should immediately recognize

- document structure
- important ideas
- executable commands
- code
- warnings
- references

without mentally parsing Markdown syntax.

---

# Out of Scope

This renderer does not introduce

- animations
- floating windows
- mouse interaction
- rich text editing
- HTML rendering

It remains a terminal-first experience.

---

# Acceptance Criteria

- Raw Markdown syntax is never visible.
- Headings render as semantic section titles.
- Lists are visually clean.
- Tables render as real tables.
- Code blocks use Tree-sitter syntax highlighting.
- Long code blocks collapse automatically.
- Inline code is distinguishable.
- Quotes and callouts become semantic widgets.
- Colors reflect importance rather than decoration.
- Rendering remains lightweight and terminal-native.

---

# Guiding Principle

Developers spend hours reading.

The renderer should reduce cognitive load,
not faithfully reproduce Markdown syntax.

Markdown is only the transport format.

The interface should communicate knowledge,
not formatting.

A developer should feel like they are reading
well-structured engineering documentation,
not raw LLM output.
