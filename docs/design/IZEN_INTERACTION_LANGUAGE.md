# Izen Interaction Language v1.0

> **AI amplifies human judgment. Humans remain in control.**
>
> Izen is not a command-line full of unrelated commands.
> It is an **Interaction Language** built around a small set of semantic markers that express user intent naturally.

---

# Design Principles

Izen follows four fundamental concepts:

```
Human Intent
      │
      ▼
Interaction Language
      │
 ┌────┼────┬─────┐
 │    │    │     │
 ▼    ▼    ▼     ▼
/    $    @   Natural Language
```

| Marker | Meaning | Example |
|----------|----------|---------|
| `/` | Workflow or Command | `/build` |
| `$` | Capability | `$hot` |
| `@` | Scope | `@internal/auth.go` |
| text | Goal | `fix login timeout` |

Unlike a traditional CLI, Izen parses **semantic markers**, not positional arguments.

These are equivalent:

```text
/build $hot fix login timeout @auth.go
```

```text
/build fix login timeout @auth.go $hot
```

```text
/build @auth.go $hot fix login timeout
```

All produce the same intent.

---

# Workflow Contexts

A workflow defines the current working context.

It is **not a command**.

It answers the question:

> "What am I trying to do?"

```
Workflow
│
├── /ask
├── /investigate
├── /plan
├── /build
└── /review
```

---

# /ask

Purpose

```
Understand
Explain
Explore
Clarify
```

Characteristics

- Read-only
- No workspace mutation
- No code changes

Capability

```
$prompt
```

Builds or refines prompts before entering another workflow.

---

# /investigate

Purpose

```
Observe

↓

Diagnose

↓

Explain
```

Characteristics

- Read-only
- Collects evidence
- Finds root causes
- Never modifies files

Capabilities

## Observe

```
$env
```

Inspect workspace environment.

Examples

- Go version
- Docker
- PATH
- Services
- Environment variables

---

```
$trace
```

Collect runtime execution information.

Examples

- Stack trace
- Call graph
- Runtime output
- Execution timeline

---

```
$log
```

Display workspace mutation history.

---

## Diagnose

```
$diagnose
```

Analyze collected evidence to identify the root cause.

---

# /plan

Purpose

```
Think

↓

Organize

↓

Produce an execution plan
```

Characteristics

- Read-only
- No workspace mutation

No dedicated capabilities.

The workflow itself is intentionally simple.

---

# /build

Purpose

```
Modify the workspace
```

This is the only workflow responsible for changing project files.

Capabilities

---

```
$hot
```

Fast targeted mutation.

Designed for small, localized edits.

Example

```
/build $hot fix typo @README.md
```

---

```
$fix
```

Structured implementation.

Used for larger fixes or feature implementation.

Example

```
/build $fix implement OAuth refresh flow
```

---

## Context Commands

Unlike workflows, these commands operate **inside** `/build`.

---

### /undo

Rollback the most recent runtime mutation.

```
S0

↓

S1

↓

S2

↓

Undo

↓

S1
```

No Git commit is created.

---

### /undo --session

Rollback every mutation made during the current build session.

```
Session Start

↓

20 edits

↓

Undo Session

↓

Initial State
```

---

### /commit

Persist the current runtime timeline into Git.

```
Runtime Timeline

↓

Git Commit
```

Git should contain only meaningful milestones.

---

# /review

Purpose

```
Validate

↓

Verify

↓

Report
```

Characteristics

- Read-only
- No workspace mutation

Capabilities

---

```
$run
```

Execute the application.

---

```
$test
```

Run project tests.

---

```
$log
```

Review mutation history.

---

# Runtime Timeline

One of Izen's core architectural ideas.

Git history and runtime history are separate.

```
Runtime Timeline

S0

↓

S1

↓

S2

↓

S3

↓

Commit

↓

Git
```

Runtime supports

- undo
- rollback
- retry

Git stores only completed work.

---

# Workspace Explorer

Workspace explorers never modify the project.

---

## /arch

Explore the project architecture.

Examples

```
/arch
```

Overview

---

```
/arch Infrastructure
```

Explore a layer.

---

```
/arch database
```

Inspect a package.

---

```
/arch --all
```

Display the complete architecture map.

---

# Runtime Commands

These commands inspect the runtime rather than the project.

---

## /usage

Display runtime usage information.

Example output

```
Provider

Model

Input Tokens

Output Tokens

Context Window

Session Cost

Provider Status
```

Purpose

- Monitor token usage
- Inspect runtime configuration
- View provider availability
- Inspect current model

---

## /model

Manage or switch AI providers and models.

Provider switching may occur automatically depending on routing policy.

---

# Workspace Sessions

Izen treats sessions as **workspace state**, not chat history.

A session preserves

- workflow
- runtime
- scopes
- mutation timeline
- model
- provider
- knowledge cache

Example

```
/session
```

List available sessions.

---

```
/session auth-refactor
```

Resume a previous workspace.

---

```
/session new payment-v2
```

Create a new workspace session.

---

Sessions are stored under

```
.izen/
```

allowing work to be resumed at any time.

---

# Global Commands

These commands are available regardless of the current workflow.

---

## /help

Display command reference.

---

## /usage

Display runtime statistics.

---

## /model

Manage models and providers.

---

## /session

Manage workspace sessions.

---

## /arch

Explore workspace architecture.

---

## /clear

Clear the current terminal output.

Does not modify workspace state.

---

## /drop

Drop the current conversation or runtime context.

Workspace files remain unchanged.

---

# Interaction Grammar

Izen parses semantic markers rather than positional arguments.

```
Input

↓

Workflow (/)

↓

Capability ($)

↓

Scope (@)

↓

Goal
```

Markers may appear anywhere.

---

# Capability Matrix

```
┌──────────────────────────────────────────────────────┐
│ /ask                                                 │
├──────────────────────────────────────────────────────┤
│ Explain                                              │
│ Clarify                                              │
│ Prompt ($prompt)                                     │
│ Read Only                                            │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ /investigate                                         │
├──────────────────────────────────────────────────────┤
│ Observe                                              │
│   $env                                               │
│   $trace                                             │
│   $log                                               │
│                                                      │
│ Diagnose                                             │
│   $diagnose                                          │
│                                                      │
│ Read Only                                            │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ /plan                                                │
├──────────────────────────────────────────────────────┤
│ Planning                                             │
│ Strategy                                             │
│ Read Only                                            │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ /build                                               │
├──────────────────────────────────────────────────────┤
│ $hot                                                 │
│ $fix                                                 │
│ /undo                                                │
│ /undo --session                                      │
│ /commit                                              │
│ Workspace Mutation                                   │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│ /review                                              │
├──────────────────────────────────────────────────────┤
│ $run                                                 │
│ $test                                                │
│ $log                                                 │
│ Validation                                           │
└──────────────────────────────────────────────────────┘
```

---

# Architecture Summary

```
                           Human Intent
                                 │
                                 ▼
                    Izen Interaction Language
                                 │
         ┌──────────────┬──────────────┬──────────────┐
         │              │              │              │
         ▼              ▼              ▼              ▼
     Workflow       Capability       Scope          Goal
        (/)            ($)            (@)      Natural Language
         │
         ▼
 ┌───────────────────────────────────────────────┐
 │ Workflow Context                              │
 ├───────────────────────────────────────────────┤
 │ /ask                                          │
 │ /investigate                                  │
 │ /plan                                         │
 │ /build                                        │
 │ /review                                       │
 └───────────────────────────────────────────────┘
         │
         ▼
 ┌───────────────────────────────────────────────┐
 │ Runtime Timeline                              │
 ├───────────────────────────────────────────────┤
 │ Mutation                                      │
 │ Undo                                          │
 │ Retry                                         │
 │ Commit                                        │
 └───────────────────────────────────────────────┘
         │
         ▼
 ┌───────────────────────────────────────────────┐
 │ Git Timeline                                  │
 ├───────────────────────────────────────────────┤
 │ feat                                          │
 │ fix                                           │
 │ refactor                                      │
 └───────────────────────────────────────────────┘
```

---

# Interaction Constitution

1. Workflow defines context.
2. Capabilities never change workflow.
3. Scope is semantic, not positional.
4. Parsers recognize markers instead of argument order.
5. Runtime Timeline is independent from Git history.
6. Git records milestones, not every AI mutation.
7. Workspace explorers are always read-only.
8. Autocomplete teaches the language instead of forcing users to memorize commands.
9. Keep workflows few, stable, and long-lived.
10. Grow engine intelligence before introducing new commands.
